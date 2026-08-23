package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// ChatReply is what the client surfaces: the answer, any reasoning the model
// produced, and the session/usage facts needed to render the status header.
// Pending is set when the agent paused at a human-in-the-loop checkpoint.
type ChatReply struct {
	Reply            string
	ReasoningContent string
	SessionID        uuid.UUID
	Model            string
	ContextWindow    int
	Usage            *contracts.TokenUsage
	Pending          *Approval
}

// Approval is a human-in-the-loop checkpoint the agent reached; the turn
// pauses until the user decides.
type Approval struct {
	ID      uuid.UUID
	Message string
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
	CreateSession(ctx context.Context, opts SessionCreateOptions) (*store.SessionMeta, error)
	LoadSession(ctx context.Context, id uuid.UUID) (*store.SessionMeta, error)
	GetSession(ctx context.Context, id uuid.UUID) (*store.SessionMeta, []store.Record, error)
	DeleteSession(ctx context.Context, id uuid.UUID) error
	ListSessions(ctx context.Context) ([]store.SessionMeta, error)
	ListProviders(ctx context.Context) ([]store.Provider, error)
	SetSessionModel(ctx context.Context, provider, model string) error
	Compact(ctx context.Context) (*store.SessionMeta, error)
	GetTimeline(ctx context.Context, id uuid.UUID) (*TimelineView, error)
	SelectBranch(ctx context.Context, id, eventID uuid.UUID) error
}

type TimelineEvent struct {
	ID         uuid.UUID        `json:"id"`
	ParentID   uuid.UUID        `json:"parent_id"`
	BranchFrom *uuid.UUID       `json:"branch_from,omitempty"`
	Kind       store.RecordKind `json:"kind"`
	BlobHash   string           `json:"blob_hash,omitempty"`
	Record     *store.Record    `json:"record,omitempty"`
}

type TimelineView struct {
	Meta   store.SessionMeta `json:"meta"`
	Head   uuid.UUID         `json:"head"`
	Events []TimelineEvent   `json:"events"`
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
	scanner := bufio.NewScanner(in)

	// Startup header.
	fmt.Fprintln(out, "infai agent")
	cSystem.Fprintln(out, "  /help shortcuts · /model switch model · /sessions list · /quit exit")

	// Fail fast on an unreachable server, and pick what to do on launch.
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach server: %v (is `agent server` running?)", err)
	}

	// Interactive terminal → the TUI. Session/model selection happens inside
	// the TUI through floating popups, so the user never sees a plain-text
	// numbered prompt; the whole launch is the chat interface.
	if term.IsTerminal(os.Stdout.Fd()) {
		return runChatTUI(ctx, c, sessions, opts)
	}

	// Non-interactive → the plain line REPL: resume the explicitly requested
	// session, or let the user pick one (or a new session) by number.
	var resume uuid.UUID
	if opts.SessionID != uuid.Nil {
		resume = opts.SessionID
	} else if len(sessions) > 0 {
		resume, err = pickSession(ctx, c, out, scanner, sessions)
		if err != nil {
			return err
		}
	}
	if resume != uuid.Nil {
		meta, err := c.LoadSession(ctx, resume)
		if err != nil {
			Notice(out, "Error", err.Error())
			return err
		}
		c.SetSession(meta.ID)
		state.session = *meta
	} else {
		// New session: pick the provider/model now, not on the first message.
		meta, err := ensureSession(ctx, c, out, scanner, opts)
		if err != nil {
			Notice(out, "Error", err.Error())
			return err
		}
		state.session = *meta
	}

	// Resumed session: print the history first, then drop into the loop.
	if resume != uuid.Nil {
		if _, records, err := c.GetSession(ctx, resume); err == nil {
			renderHistory(out, records)
		}
		cSystem.Fprintf(out, "resumed session %s\n", resume)
	}

	for {
		// Footer (bottom status bar) then editor prompt, like Pi's regular
		// mode: the terminal scrolls the transcript above them.
		renderStatus(out, state)
		cPrompt.Fprint(out, "> ")

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

		cUser.Fprintf(out, "▶ %s\n", prompt)
		fmt.Fprintln(out)

		thinkingShown := false
		contentStarted := false
		reply, err := c.Chat(ctx, prompt, func(kind contracts.DeltaKind, text string) {
			switch kind {
			case contracts.DeltaReasoning:
				if !thinkingShown {
					cThinking.Fprintln(out, "─ thinking ─")
					thinkingShown = true
				}
				cThinking.Fprint(out, text)
			case contracts.DeltaContent:
				if thinkingShown {
					cHeader.Fprintln(out, "────────")
				}
				if !contentStarted {
					cAssistant.Fprint(out, "● ")
					contentStarted = true
				}
				cAssistant.Fprint(out, text)
			case contracts.DeltaStatus:
				cSystem.Fprintln(out, statusLabel(text))
			}
		})
		if thinkingShown && !contentStarted {
			cHeader.Fprintln(out, "────────")
		}
		if err != nil {
			Notice(out, "Error", err.Error())
			continue
		}
		fmt.Fprintln(out)

		updateState(state, reply)
	}

	return scanner.Err()
}

// renderHistory prints a saved session's active timeline (message records in order)
// so a resumed session reads like the live chat. Tool records and the system
// prompt are skipped.
func renderHistory(out io.Writer, records []store.Record) {
	for _, rec := range records {
		if rec.Kind != store.KindMessage || rec.Message == nil {
			continue
		}
		m := rec.Message
		switch m.Role {
		case "user":
			cUser.Fprintf(out, "▶ %s\n", m.Text())
			fmt.Fprintln(out)
		case "assistant":
			if m.ReasoningContent != "" {
				cThinking.Fprintln(out, "─ thinking ─")
				cThinking.Fprintln(out, m.ReasoningContent)
				cHeader.Fprintln(out, "────────")
			}
			cAssistant.Fprintf(out, "● %s\n", m.Text())
			fmt.Fprintln(out)
		}
	}
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

// renderStatus prints the footer status bar (model, session, context, turns).
func renderStatus(out io.Writer, s *replState) {
	if s.session.ID == uuid.Nil {
		cHeader.Fprintln(out, "no session · edit models.json to configure providers/models")
		return
	}
	pct := ""
	if s.session.ContextWindow > 0 {
		pct = fmt.Sprintf(" (%d%%)", s.used*100/s.session.ContextWindow)
	}
	line := strings.Join([]string{
		"model: " + s.session.Model,
		"sess: " + s.session.ID.String(),
		fmt.Sprintf("ctx: %d/%d%s", s.used, s.session.ContextWindow, pct),
		fmt.Sprintf("turns: %d", s.session.TurnCount),
	}, "  ·  ")
	cHeader.Fprintln(out, line)
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
	  /compact                       compact the current conversation
  /branch-timeline               inspect and select a branch point
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

	case "/compact":
		if s.session.ID == uuid.Nil {
			return false, fmt.Errorf("no active session")
		}
		meta, err := c.Compact(ctx)
		if err != nil {
			return false, err
		}
		s.session = *meta
		fmt.Fprintln(out, statusLabel("compacted"))
		return false, nil

	case "/branch-timeline":
		return false, runBranchTimeline(ctx, c, out, s, scan)

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

func runBranchTimeline(ctx context.Context, c Client, out io.Writer, s *replState, scan *bufio.Scanner) error {
	if s.session.ID == uuid.Nil {
		return fmt.Errorf("no active session")
	}
	view, err := c.GetTimeline(ctx, s.session.ID)
	if err != nil {
		return err
	}
	parents := make(map[uuid.UUID]uuid.UUID, len(view.Events))
	byID := make(map[uuid.UUID]TimelineEvent, len(view.Events))
	options := make([]TimelineEvent, 0, len(view.Events))
	for _, event := range view.Events {
		parents[event.ID] = event.ParentID
		byID[event.ID] = event
		options = append(options, event)
	}
	for i, event := range options {
		fmt.Fprintf(out, "  %d  %s%s\n", i, timelineTreePrefix(event, parents, byID), timelineEventLabel(event))
	}
	fmt.Fprint(out, "branch at> ")
	if !scan.Scan() {
		return scan.Err()
	}
	idx, err := strconv.Atoi(strings.TrimSpace(scan.Text()))
	if err != nil || idx < 0 || idx >= len(options) {
		return fmt.Errorf("invalid timeline selection")
	}
	if err := c.SelectBranch(ctx, s.session.ID, options[idx].ID); err != nil {
		return err
	}
	fmt.Fprintln(out, branchSelectionLabel(options[idx]))
	return nil
}

func statusLabel(status string) string {
	switch status {
	case "compacting":
		return "⟳ compacting conversation"
	case "compacted":
		return "✓ conversation compacted"
	default:
		return status
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

	// Align the model column so the provider reads as a table.
	maxModel := 0
	for _, o := range opts {
		if len(o.model) > maxModel {
			maxModel = len(o.model)
		}
	}
	cPrompt.Fprintln(out, "select a model:")
	for i, o := range opts {
		fmt.Fprintf(out, "  %d  %-*s @ %s\n", i, maxModel, o.model, o.provider)
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

// pickSession lists the engine's saved sessions and reads a numbered choice:
// 0 starts a new session, 1..N resume an existing one. Returns uuid.Nil for a
// new session.
func pickSession(ctx context.Context, c Client, out io.Writer, scan *bufio.Scanner, sessions []store.SessionMeta) (uuid.UUID, error) {
	cSystem.Fprintln(out, "sessions:")
	fmt.Fprintf(out, "  %d  start a new session\n", 0)
	for i, m := range sessions {
		fmt.Fprintf(out, "  %d  %s  %s  %s\n", i+1, m.ID, m.Model, humanTime(m.UpdatedAt))
	}
	cPrompt.Fprint(out, "> ")

	if !scan.Scan() {
		return uuid.Nil, scan.Err()
	}
	choice := strings.TrimSpace(scan.Text())
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx > len(sessions) {
		return uuid.Nil, fmt.Errorf("invalid selection %q", choice)
	}
	if idx == 0 {
		return uuid.Nil, nil
	}
	return sessions[idx-1].ID, nil
}

// humanTime renders a timestamp in a short local format ("Jan 02 15:04").
func humanTime(t time.Time) string {
	return t.Local().Format("Jan 02 15:04")
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
	if len(s) > 12 {
		return s[:4] + "-" + s[len(s)-8:]
	}
	return s
}

// orModel returns a displayable model label, falling back when the session was
// never configured with one.
func orModel(m string) string {
	if m == "" {
		return "no model"
	}
	return m
}
