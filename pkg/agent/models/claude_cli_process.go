package models

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

const (
	// claudeMaxAttempts bounds retries of a transient provider failure. Auth
	// and rate-limit failures are never retried.
	claudeMaxAttempts = 2
	// claudeCLIMemoryMax bounds a turn's resident memory. Measured peaks are
	// ~287 MB for a trivial turn and ~291 MB for a 140 KB prompt: the runtime,
	// not the context, dominates. 512 MiB leaves roughly 220 MB of headroom,
	// so a long context has room without the cap being merely decorative.
	claudeCLIMemoryMax = 512 << 20
	// claudeCLICPUQuota and claudeCLICPUPeriod give the turn 500m: 50 ms of CPU
	// per 100 ms, i.e. half a core. CPU is throttled, never killed.
	claudeCLICPUQuota  = 50000
	claudeCLICPUPeriod = 100000
	// claudeCLIPidsMax stops a fork storm; the CLI needs only a handful.
	claudeCLIPidsMax = 256
)

var execLookPath = exec.LookPath

// claudeSessionArg selects the CLI conversation a turn joins. A fresh turn
// opens a named session; a resumed turn appends to the one the CLI already
// stored, which keeps its message list append-only and therefore cacheable.
type claudeSessionArg struct {
	ID     string
	Resume bool
}

// claudeCLIArgs builds the argument list. Built-in tools stay disabled and MCP
// is strictly empty: the CLI must not run anything of its own. Do not add
// --bare, which drops the subscription login this provider relies on.
//
// Session persistence stays on: the stored message list is what lets the CLI
// reuse its prompt cache instead of re-sending the whole transcript as one
// rewritten user message.
func claudeCLIArgs(model contracts.ProvisionedModel, systemPrompt, schema string, streamJSON bool, session claudeSessionArg) []string {
	args := []string{
		"-p",
		"--tools", "",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "",
		"--disable-slash-commands",
	}
	if session.Resume {
		args = append(args, "--resume", session.ID)
	} else {
		args = append(args, "--session-id", session.ID)
	}
	args = append(args,
		"--system-prompt", systemPrompt,
		"--model", model.Model().Id,
		"--json-schema", schema,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	)
	if streamJSON {
		args = append(args, "--input-format", "stream-json")
	}
	if effort, ok := model.ThinkingLevelValue(); ok {
		args = append(args, "--effort", effort)
	}
	return args
}

func (c *claudeCLI) run(ctx context.Context, systemPrompt string, turn claudeTurn, attempt int, schema string, allowed claudeTools, opts *contracts.GenerateOptions) (claudeStreamResult, error) {
	binary, err := c.resolveBinary()
	if err != nil {
		return claudeStreamResult{}, err
	}
	input := turn.full
	session := claudeSessionArg{ID: turn.sessionID}
	switch {
	case attempt > 1:
		// The failed attempt may already have created the assigned id, so a
		// retry opens a new conversation with the full history.
		session = claudeSessionArg{ID: uuid.NewString()}
	case turn.resume:
		input = turn.delta
		session = claudeSessionArg{ID: turn.sessionID, Resume: true}
	}
	stdin, streamJSON := claudeStdin(input.transcript, input.images)
	cmd := exec.Command(binary, claudeCLIArgs(c.model, systemPrompt, schema, streamJSON, session)...)
	cmd.Dir = c.opts.workspace
	// Inherit the environment so the CLI finds its own login; infai neither
	// injects nor logs an auth variable.
	//
	// Auto-memory is off. Now that the CLI runs in the session workspace it
	// would inject the user's ~/.claude/projects/<slug>/memory/MEMORY.md into
	// the first user message. That content is not part of this conversation,
	// and infai already owns memory and skills. Settings are disabled
	// (--setting-sources ""), so the env var is the lever.
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	configureClaudeProcess(cmd)
	cmd.Stdin = strings.NewReader(stdin)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return claudeStreamResult{}, fmt.Errorf("claude cli: stdout pipe: %w", err)
	}
	stderr := newClaudeTailBuffer(64 * 1024)
	cmd.Stderr = stderr

	// Bound the turn with a cgroup when the unit delegated one to us. This is
	// containment, not isolation: the CLI still runs as the user. A host that
	// has not delegated runs unconstrained, exactly as before.
	cgroup, _ := newClaudeCgroup(claudeCgroupLimits{
		CPUMax:    fmt.Sprintf("%d %d", claudeCLICPUQuota, claudeCLICPUPeriod),
		MemoryMax: c.opts.memoryMax,
		PidsMax:   c.opts.pidsMax,
	})
	releaseCgroup := func() {}
	if cgroup != nil {
		defer cgroup.remove()
		if release, err := cgroup.attach(cmd); err == nil {
			releaseCgroup = release
		}
	}

	if err := cmd.Start(); err != nil {
		releaseCgroup()
		return claudeStreamResult{}, fmt.Errorf("claude cli: start: %w", err)
	}
	// The clone has happened; the directory handle is no longer needed.
	releaseCgroup()

	// A native tool call ends the turn from the reader, so the process has to
	// be terminated to unblock Wait. The cause stays local: the caller asked to
	// cancel only when it cancels ctx, not when it supplies a tool call.
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	done := make(chan struct{})
	go func() {
		select {
		case <-turnCtx.Done():
			terminateClaudeProcess(cmd.Process)
		case <-done:
		}
	}()
	result, decodeErr := decodeClaudeStream(turnCtx, stdout, opts, allowed, cancelTurn)
	result.SessionID = session.ID
	waitErr := cmd.Wait()
	close(done)

	if result.NativeTool != nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if decodeErr != nil {
		if errors.Is(decodeErr, harnessErr.ErrTurnCanceled) {
			return result, decodeErr
		}
		if isClaudeAuthError(decodeErr.Error() + " " + stderr.String()) {
			return result, claudeAuthError()
		}
		return result, decodeErr
	}
	if waitErr != nil || !result.ResultSeen {
		if cgroup != nil && c.opts.memoryMax > 0 {
			if kills, ok := cgroup.oomKills(); ok && kills > 0 {
				return result, fmt.Errorf("claude cli: the turn was killed by its memory limit of %d MiB", c.opts.memoryMax>>20)
			}
		}
		message := stderr.String()
		if isClaudeAuthError(message) {
			return result, claudeAuthError()
		}
		if session.Resume && isClaudeMissingSession(message) {
			// The stored conversation is gone (cleanup, or a session file that
			// never landed). Let the retry open a fresh one.
			result.Retryable = true
		}
		if !session.Resume && isClaudeSessionInUse(message) {
			// The assigned id already names a conversation from an earlier
			// process. Retry as a new generation.
			result.Retryable = true
		}
		if message == "" {
			message = "the CLI exited without a result event"
		}
		return result, fmt.Errorf("claude cli: %s", message)
	}
	return result, nil
}

// isClaudeAuthError recognises the CLI's own "you are not signed in" wording.
func isClaudeAuthError(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"not logged in", "not signed in", "authentication", "unauthorized"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func claudeAuthError() error {
	return errors.New("Claude Code isn't signed in. Run `claude` and use /login, then try again.")
}

func isClaudeSessionInUse(message string) bool {
	return strings.Contains(strings.ToLower(message), "already in use")
}

func isClaudeMissingSession(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"no conversation found", "session not found", "no such session"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isClaudeTransientError(message string) bool {
	message = strings.ToLower(message)
	if strings.Contains(message, "api_error_status") {
		return true
	}
	for _, marker := range []string{"overload", "rate_limit", "too many requests", " 500", " 502", " 503", " 504", " 529"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// claudeTailBuffer keeps the last max bytes written to it, so an error message
// can quote the CLI's own stderr without holding an unbounded amount of it.
type claudeTailBuffer struct {
	max int
	buf []byte
}

func newClaudeTailBuffer(max int) *claudeTailBuffer { return &claudeTailBuffer{max: max} }

func (b *claudeTailBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return written, nil
	}
	if excess := len(b.buf) + len(p) - b.max; excess > 0 {
		copy(b.buf, b.buf[excess:])
		b.buf = b.buf[:len(b.buf)-excess]
	}
	b.buf = append(b.buf, p...)
	return written, nil
}

func (b *claudeTailBuffer) String() string { return string(bytes.TrimSpace(b.buf)) }

var _ io.Writer = (*claudeTailBuffer)(nil)

// claudeScanner reads the CLI's newline-delimited JSON, which can hold large
// lines: a single event carries a whole assistant message.
func claudeScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return scanner
}
