package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// ChatReply is what the client surfaces: the answer, any reasoning the model
// produced, and the session/usage facts needed to render the status header.
type ChatReply struct {
	Reply            string
	ReasoningContent string
	SessionID        uuid.UUID
	Model            string
	ContextWindow    int
	Usage            *contracts.TokenUsage
}

// SessionCreateOptions describes a new session for the server.
type SessionCreateOptions struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Cwd      string `json:"cwd"`
}

// Client is the CLI's view of the engine. RemoteClient is the HTTP transport
// to a running <binary> server.
type Client interface {
	Chat(ctx context.Context, prompt string, onDelta func(kind contracts.DeltaKind, text string)) (*ChatReply, error)
	SetSession(id uuid.UUID)
	SessionID() uuid.UUID
	CreateSession(ctx context.Context, opts SessionCreateOptions) (*store.SessionMeta, error)
	LoadSession(ctx context.Context, id uuid.UUID) (*store.SessionMeta, error)
	DeleteSession(ctx context.Context, id uuid.UUID) error
	ListSessions(ctx context.Context) ([]store.SessionMeta, error)
	ListProviders(ctx context.Context) ([]store.Provider, error)
	SetSessionModel(ctx context.Context, provider, model string) error
}

// RunOptions controls REPL startup.
type RunOptions struct {
	// SessionID resumes a saved session when set; otherwise a fresh session is
	// created.
	SessionID uuid.UUID
}

// replState carries the mutable view the REPL renders in its status header.
type replState struct {
	session store.SessionMeta
	used    int // accumulated prompt+completion tokens across the run
}

// Run is the CLI entry point: a plain-stdio interactive REPL.
func Run(ctx context.Context, c Client, in io.Reader, out io.Writer, opts RunOptions) error {
	return runLine(ctx, c, in, out, opts)
}

// runLine is the plain-stdio REPL (used when stdout is not a terminal).
func runLine(ctx context.Context, c Client, in io.Reader, out io.Writer, opts RunOptions) error {
	state := &replState{}

	if opts.SessionID != uuid.Nil {
		meta, err := c.LoadSession(ctx, opts.SessionID)
		if err != nil {
			Notice(out, "Error", err.Error())
			return err
		}
		c.SetSession(meta.ID)
		state.session = *meta
		fmt.Fprintf(out, "infai agent — resumed session %s.\n", meta.ID)
		fmt.Fprintln(out, "type a message, /help for commands, or /quit to leave.")
	} else {
		// Fail fast on an unreachable server, but do NOT require a session or
		// provider yet: on first run the user adds a provider from inside the
		// REPL, and the session is created lazily on the first real message.
		if _, err := c.ListProviders(ctx); err != nil {
			return fmt.Errorf("cannot reach server: %v (is `agent server` running?)", err)
		}
		fmt.Fprintln(out, "infai agent — no session yet. Type a message to start one, or configure a model first:")
		fmt.Fprintln(out, "  edit models.json to add providers/models, then restart the server")
		fmt.Fprintln(out, "/help for all commands, /quit to leave.")
	}

	scanner := bufio.NewScanner(in)
	echo := isTerminal(in)
	for {
		renderStatus(out, state)

		if !scanner.Scan() {
			break
		}
		first := scanner.Text()
		if strings.TrimSpace(first) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(first), "/") {
			if done, err := runCommand(ctx, c, out, state, strings.TrimSpace(first), scanner); err != nil {
				Notice(out, "Error", err.Error())
			} else if done {
				return nil
			}
			continue
		}

		// Gather a message: a line ending with "\" continues onto the next
		// line (multi-line prompts); a blank line ends a continuation. A
		// single plain line sends immediately.
		lines := []string{first}
		for strings.HasSuffix(strings.TrimRight(lines[len(lines)-1], " \t"), "\\") {
			lines[len(lines)-1] = strings.TrimSuffix(strings.TrimRight(lines[len(lines)-1], " \t"), "\\")
			cPrompt.Fprint(out, "> ")
			if !scanner.Scan() {
				break
			}
			next := scanner.Text()
			if strings.TrimSpace(next) == "" {
				break
			}
			lines = append(lines, next)
		}
		prompt := strings.Join(lines, "\n")

		// Lazily create the session on the first real message, so provider
		// management commands work before any model is configured.
		if state.session.ID == uuid.Nil {
			meta, err := ensureSession(ctx, c, out, scanner, opts)
			if err != nil {
				Notice(out, "Error", err.Error())
				continue
			}
			state.session = *meta
			fmt.Fprintf(out, "\nnew session %s.\n", meta.ID)
		}

		// In a terminal the typed input is already echoed next to the "> "
		// prompt; when not (piped), label the message so it stays readable.
		if !echo {
			cUser.Fprintf(out, "you: %s\n", prompt)
		}
		fmt.Fprintln(out)

		thinkingOpen := false
		thinkingEndedNewline := false
		closeThinking := func() {
			if thinkingOpen {
				if !thinkingEndedNewline {
					fmt.Fprintln(out)
				}
				cHeader.Fprintln(out, "────────")
				thinkingOpen = false
			}
		}
		replyStarted := false
		reply, err := c.Chat(ctx, prompt, func(kind contracts.DeltaKind, text string) {
			switch kind {
			case contracts.DeltaReasoning:
				if !thinkingOpen {
					cThinking.Fprintln(out, "─ thinking ─")
					thinkingOpen = true
				}
				cThinking.Fprint(out, text)
				thinkingEndedNewline = strings.HasSuffix(text, "\n")
			case contracts.DeltaContent:
				closeThinking()
				if !replyStarted {
					cAssistantLabel.Fprint(out, "assistant: ")
					replyStarted = true
				}
				cAssistant.Fprint(out, text)
			}
		})
		if err != nil {
			closeThinking()
			Notice(out, "Error", err.Error())
			continue
		}
		closeThinking()
		fmt.Fprintln(out)

		updateState(state, reply)
	}

	return scanner.Err()
}

// ensureSession has the user pick a provider and model, creates the session on
// the server and wires it into the client. It is the lazy path used on the
// first real message and by /new, so the REPL works before any model is
// configured.
func ensureSession(ctx context.Context, c Client, out io.Writer, scan *bufio.Scanner, opts RunOptions) (*store.SessionMeta, error) {
	provider, model, err := chooseModel(ctx, c, out, scan)
	if err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	meta, err := c.CreateSession(ctx, SessionCreateOptions{
		Provider: provider,
		Model:    model,
		Cwd:      cwd,
	})
	if err != nil {
		return nil, err
	}
	c.SetSession(meta.ID)
	return meta, nil
}

// updateState folds a ChatReply into the REPL state. Token consumption comes
// from the model's reply usage, accumulated in memory for the header; it is
// never persisted to the session.
func updateState(s *replState, reply *ChatReply) {
	if reply.Usage != nil {
		s.used += reply.Usage.PromptTokens + reply.Usage.CompletionTokens
	}

	if reply.Model != "" {
		s.session.Model = reply.Model
	}
	if reply.ContextWindow > 0 {
		s.session.ContextWindow = reply.ContextWindow
	}
	if reply.SessionID != uuid.Nil {
		s.session.ID = reply.SessionID
	}
	s.session.TurnCount++
}

// renderStatus prints the right-aligned status header above the prompt.
func renderStatus(out io.Writer, s *replState) {
	seg := []string{
		"model: " + s.session.Model,
		"sess: " + s.session.ID.String(),
		fmt.Sprintf("ctx: %d/%d", s.used, s.session.ContextWindow),
		fmt.Sprintf("turns: %d", s.session.TurnCount),
	}
	line := strings.Join(seg, "  ·  ")

	if s.session.ID == uuid.Nil {
		cHeader.Fprintf(out, "%s\n", "no session · edit models.json to configure providers/models")
		cPrompt.Fprint(out, "> ")
		return
	}

	width := 80
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		width = w
	}
	if len(line) < width {
		line = strings.Repeat(" ", width-len(line)) + line
	}
	cHeader.Fprintln(out, line)
	cPrompt.Fprint(out, "> ")
}

// isTerminal reports whether r is a terminal (i.e. the user's typed input is
// echoed back, so the REPL need not print it a second time).
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// runCommand handles a slash command. scan lets interactive commands (the
// /model picker) read a follow-up line from the same input stream. Returns
// done=true when the client should exit.
func runCommand(ctx context.Context, c Client, out io.Writer, s *replState, line string, scan *bufio.Scanner) (bool, error) {
	fields := strings.Fields(line)
	cmd := fields[0]
	args := fields[1:]

	switch cmd {
	case "/help":
		fmt.Fprintln(out, `commands:
  /help                          show this help
  /providers                     list configured providers and their models
  /model                         pick a model from a numbered list
  /model <provider> <model>      switch the session to a provider's model
  /sessions                      list saved sessions
  /session load <uuid>           resume a saved session
  /session rm <uuid>             delete a saved session
  /new                           start a fresh session
  /ctx                           show context used vs total
  /pwd                           show the session working directory
  /quit, /exit                   leave

multi-line: end a line with \ to continue typing on the next line`)
		return false, nil

	case "/providers":
		provs, err := c.ListProviders(ctx)
		if err != nil {
			return false, err
		}
		if len(provs) == 0 {
			fmt.Fprintln(out, "no providers configured — add providers/models in models.json and restart the server")
			return false, nil
		}
		for _, p := range provs {
			fmt.Fprintf(out, "%-16s %-28s %-8s models: %s\n",
				p.Name, p.Endpoint, p.APIType, strings.Join(p.ModelNames(), ", "))
		}
		return false, nil

	case "/model":
		if len(args) == 0 {
			return false, pickModel(ctx, c, out, s, scan)
		}
		if len(args) == 2 {
			return false, setModelFor(ctx, c, out, s, args[0], args[1])
		}
		return false, fmt.Errorf("usage: /model | /model <provider> <model>")

	case "/sessions":
		sess, err := c.ListSessions(ctx)
		if err != nil {
			return false, err
		}
		if len(sess) == 0 {
			fmt.Fprintln(out, "no saved sessions")
			return false, nil
		}
		for _, m := range sess {
			fmt.Fprintf(out, "%-36s %-20s turns=%d updated=%s\n",
				m.ID, m.Model, m.TurnCount, m.UpdatedAt)
		}
		return false, nil

	case "/session":
		return runSessionCmd(ctx, c, out, s, args)

	case "/new":
		meta, err := ensureSession(ctx, c, out, scan, RunOptions{})
		if err != nil {
			return false, err
		}
		s.session = *meta
		s.used = 0
		fmt.Fprintf(out, "new session %s\n", meta.ID)
		return false, nil

	case "/ctx":
		fmt.Fprintf(out, "context used: %d / %d\n", s.used, s.session.ContextWindow)
		return false, nil

	case "/pwd":
		if s.session.ID == uuid.Nil {
			return false, fmt.Errorf("no active session")
		}
		fmt.Fprintln(out, s.session.Cwd)
		return false, nil

	case "/quit", "/exit":
		return true, nil

	default:
		return false, fmt.Errorf("unknown command %q — /help for options", cmd)
	}
}

// setModelFor switches the active session to the given provider/model. An
// empty model picks the provider's first model.
func setModelFor(ctx context.Context, c Client, out io.Writer, s *replState, provider, model string) error {
	if s.session.ID == uuid.Nil {
		return fmt.Errorf("no active session — type a message or /new to start one first")
	}
	if err := c.SetSessionModel(ctx, provider, model); err != nil {
		return err
	}
	s.session.Provider = provider
	if model != "" {
		s.session.Model = model
	}
	fmt.Fprintf(out, "session model set to %s @ %s\n", model, provider)
	return nil
}

// chooseModel lists every configured model@provider and reads a numbered
// selection from the input stream, returning the chosen provider and model.
func chooseModel(ctx context.Context, c Client, out io.Writer, scan *bufio.Scanner) (string, string, error) {
	providers, err := c.ListProviders(ctx)
	if err != nil {
		return "", "", err
	}

	type option struct {
		provider, model string
	}
	var opts []option
	for _, p := range providers {
		for _, name := range p.ModelNames() {
			opts = append(opts, option{p.Name, name})
		}
	}
	if len(opts) == 0 {
		return "", "", fmt.Errorf("no models configured — add models in models.json and restart the server")
	}

	cPrompt.Fprintln(out, "select a model:")
	for i, o := range opts {
		fmt.Fprintf(out, "  %d  %s @ %s\n", i, o.model, o.provider)
	}
	cPrompt.Fprint(out, "> ")

	if !scan.Scan() {
		return "", "", scan.Err()
	}
	choice := strings.TrimSpace(scan.Text())
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(opts) {
		return "", "", fmt.Errorf("invalid selection %q", choice)
	}
	return opts[idx].provider, opts[idx].model, nil
}

// pickModel is the /model no-arg handler: choose a model, then switch the
// active session to it.
func pickModel(ctx context.Context, c Client, out io.Writer, s *replState, scan *bufio.Scanner) error {
	provider, model, err := chooseModel(ctx, c, out, scan)
	if err != nil {
		return err
	}
	return setModelFor(ctx, c, out, s, provider, model)
}

func runSessionCmd(ctx context.Context, c Client, out io.Writer, s *replState, args []string) (bool, error) {
	if len(args) == 0 {
		return false, fmt.Errorf("usage: /session load <uuid> | rm <uuid>")
	}
	sub := args[0]
	switch sub {
	case "load":
		if len(args) != 2 {
			return false, fmt.Errorf("usage: /session load <uuid>")
		}
		id, err := uuid.Parse(args[1])
		if err != nil {
			return false, fmt.Errorf("invalid session id: %v", err)
		}
		meta, err := c.LoadSession(ctx, id)
		if err != nil {
			return false, err
		}
		c.SetSession(meta.ID)
		s.session = *meta
		fmt.Fprintf(out, "resumed session %s\n", id)
		return false, nil

	case "rm":
		if len(args) != 2 {
			return false, fmt.Errorf("usage: /session rm <uuid>")
		}
		id, err := uuid.Parse(args[1])
		if err != nil {
			return false, fmt.Errorf("invalid session id: %v", err)
		}
		if err := c.DeleteSession(ctx, id); err != nil {
			return false, err
		}
		fmt.Fprintf(out, "session %s deleted\n", shortID(id))
		return false, nil

	default:
		return false, fmt.Errorf("unknown /session subcommand %q", sub)
	}
}

func shortID(id uuid.UUID) string {
	s := id.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
