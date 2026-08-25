package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/fatih/color"
	"github.com/google/uuid"
)

// eventCmd is what a single uiEvent means to the chat loop.
type eventCmd int

const (
	cmdKey eventCmd = iota // a printable rune
	cmdBackspace
	cmdEnter   // submit the input
	cmdNewline // insert a newline in the input
	cmdQuit
	cmdDelta // a streamed chunk from the model
	cmdDone  // the turn finished
	cmdTick  // spinner animation tick
	cmdScrollUp
	cmdScrollDown
	cmdMouse // a mouse report (button/coords)
	cmdPaste // bracketed paste completed; text lands in the input
	cmdRedraw
)

// uiEvent is either a terminal key, a scroll/mouse action, or a model-stream
// event (cmdDelta/cmdDone).
type uiEvent struct {
	cmd   eventCmd
	key   rune
	page  bool
	btn   int  // mouse button (SGR code); 0=left, 32=drag, 64/65=wheel
	x, y  int  // mouse cell, 1-based
	press bool // mouse press vs release
	kind  contracts.DeltaKind
	text  string
	paste string // pasted text for cmdPaste
	reply *ChatReply
	err   error
}

// block is one rendered conversation entry.
type block struct {
	role string // "user" | "thinking" | "assistant"
	text string
}

// histRow is one rendered, uncolored line of the conversation, tagged with
// its style. It backs both rendering and app-level text selection.
type histRow struct {
	plain     string
	style     string // "user" | "thinking" | "assistant" | "error"
	prefixLen int    // colored chat prefix width, when present
}

// pos is a cell in the conversation content (row = index into rows).
type pos struct{ row, col int }

// chatTUI is the raw-mode, alternate-screen chat UI: history scrolls on top,
// a status bar sits above a sticky multi-line input at the very bottom.
// Floating popups (session picker, model picker, human-approval) render as a
// focus-taking overlay over the history region.
type chatTUI struct {
	ctx     context.Context
	client  Client
	session store.SessionMeta
	used    int

	width, height int
	events        chan uiEvent

	blocks []block
	input  string
	scroll int // history scroll offset from the bottom (0 = newest)
	rows   []histRow

	// floating popup (session picker, model picker, approval). While non-nil
	// it owns the keyboard; chat input is frozen underneath.
	popup       *popup
	popupOK     func(idx int) // invoked with the chosen option
	popupCancel func()        // invoked on Esc
	quitNow     bool          // graceful quit requested from a popup/command

	// app-level text selection (mouse drag), in content coordinates
	selecting bool
	selStart  pos
	selEnd    pos
	flash     string // transient notice (e.g. clipboard result)

	// bracketed paste state: everything between ESC[200~ .. ESC[201~ is
	// inserted literally (newlines stay as newlines, Enter does not submit).
	pasting  bool
	pasteBuf string

	working   bool
	workBegan time.Time
	done      chan struct{} // closed on exit to stop the tick goroutine
}

// runChatTUI enters raw mode + the alternate screen and runs the chat loop
// until the user quits. If opts.SessionID is set that session is resumed;
// otherwise the launch popup asks the user to start a new session or resume an
// existing one, and (for a new session) to pick a model.
func runChatTUI(ctx context.Context, c Client, sessions []store.SessionMeta, opts RunOptions) error {
	t := &chatTUI{
		ctx:    ctx,
		client: c,
		events: make(chan uiEvent, 256),
		input:  "",
		done:   make(chan struct{}),
	}
	defer close(t.done)

	fd := os.Stdout.Fd()
	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	defer term.Restore(fd, state)

	w, h, err := term.GetSize(fd)
	if err != nil {
		return fmt.Errorf("get terminal size: %w", err)
	}
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	t.width, t.height = w, h

	// Redraw on terminal resize so the full-width rules always match the
	// current window.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
				t.width, t.height = w, h
			}
			t.events <- uiEvent{cmd: cmdRedraw}
		}
	}()

	// ButtonEvent mouse (1002) + SGR coordinates (1006) + bracketed paste
	// (2004): wheel/click/drag reach us, and pasted text arrives as one
	// literal block (newlines preserved, Enter does not submit mid-paste).
	fmt.Fprint(os.Stdout, "\033[?1049h\033[?1002h\033[?1006h\033[?2004h")
	defer fmt.Fprint(os.Stdout, "\033[?2004l\033[?1002l\033[?1006l\033[?1049l")

	go t.readKeys()
	go t.tick()

	// Launch: resume the requested session, or let the user pick via popups.
	t.beginLaunch(sessions, opts)

	t.redraw()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-t.events:
			if t.popup != nil {
				t.popupKey(ev)
				if t.quitNow {
					return nil
				}
				continue
			}
			switch ev.cmd {
			case cmdQuit:
				return nil
			case cmdKey:
				t.input += string(ev.key)
				if t.input == "/" {
					t.showCommandPopup()
				} else {
					t.redraw()
				}
			case cmdBackspace:
				if r, n := utf8.DecodeLastRuneInString(t.input); n > 0 {
					t.input = t.input[:len(t.input)-n]
					_ = r
				}
				t.redraw()
			case cmdNewline:
				t.input += "\n"
				t.redraw()
			case cmdEnter:
				t.submit()
			case cmdDelta:
				t.scroll = 0
				switch ev.kind {
				case contracts.DeltaStatus:
					t.appendStatus(ev.text)
				case contracts.DeltaCompactionSummary:
					t.appendCompactionSummary(ev.text)
				default:
					t.appendDelta(ev.kind, ev.text)
				}
				t.redraw()
			case cmdDone:
				t.working = false
				t.scroll = 0
				if ev.err != nil {
					t.appendError(ev.err)
				} else if ev.reply != nil {
					t.used = ev.reply.ContextTokens
					t.session.TurnCount++
					t.session.Model = ev.reply.Model
					if ev.reply.Pending != nil {
						t.showApproval(ev.reply)
					}
				}
				t.redraw()
			case cmdTick:
				// Only redraw on idle ticks while working; redrawing while
				// idle would stomp on mouse text selection.
				if t.working {
					t.redraw()
				}
			case cmdScrollUp:
				t.scrollUp(ev.page)
				t.redraw()
			case cmdScrollDown:
				t.scrollDown(ev.page)
				t.redraw()
			case cmdMouse:
				t.handleMouse(ev)
				t.redraw()
			case cmdPaste:
				t.input += ev.paste
				t.redraw()
			case cmdRedraw:
				t.redraw()
			}
			if t.quitNow {
				return nil
			}
		}
	}
}

// submit sends the current input as a user message and starts a turn.
func (t *chatTUI) submit() {
	prompt := strings.TrimSpace(t.input)
	if prompt == "" || t.working {
		return
	}
	t.input = ""
	if strings.HasPrefix(prompt, "/") {
		t.runTUICommand(prompt)
		return
	}
	if t.session.ID == uuid.Nil {
		t.appendError(errors.New("no active session"))
		t.redraw()
		return
	}
	t.blocks = append(t.blocks, block{role: "user", text: prompt})
	t.scroll = 0
	t.working = true
	t.workBegan = time.Now()

	ev := make(chan uiEvent, 256)
	go func() {
		reply, err := t.client.Chat(t.ctx, prompt, func(kind contracts.DeltaKind, text string) {
			ev <- uiEvent{cmd: cmdDelta, kind: kind, text: text}
		})
		ev <- uiEvent{cmd: cmdDone, reply: reply, err: err}
		close(ev)
	}()
	go func() {
		for e := range ev {
			t.events <- e
		}
	}()

	t.redraw()
}

// ---- floating popups ----

// showPopup opens a focus-taking overlay with the given title, body ("what")
// and selectable options. ok is called with the chosen option index; cancel
// (optional) is called on Esc.
func (t *chatTUI) showPopup(title string, body []string, opts []popupOption, ok func(int), cancel func()) {
	t.showPopupMode(title, body, opts, true, ok, cancel)
}

func (t *chatTUI) showPopupMode(title string, body []string, opts []popupOption, escapable bool, ok func(int), cancel func()) {
	t.popup = newPopup(title, body, opts, escapable)
	t.popupOK = ok
	t.popupCancel = cancel
	t.selecting = false
	t.redraw()
}

// selectPopup resolves the popup with the given option and runs its callback.
func (t *chatTUI) selectPopup(idx int) {
	ok := t.popupOK
	t.popup = nil
	t.popupOK = nil
	t.popupCancel = nil
	if ok != nil {
		ok(idx)
	}
	t.redraw()
}

// popupKey routes keyboard input to the open popup; every command is consumed
// so the chat input stays frozen underneath.
func (t *chatTUI) popupKey(ev uiEvent) {
	p := t.popup
	switch ev.cmd {
	case cmdMouse:
		if ev.btn != 0 {
			return
		}
		idx, ok := p.optionAt(ev.x-1, ev.y-1)
		if !ok {
			p.mouseSel = -1
			return
		}
		if ev.press {
			p.sel = idx
			p.mouseSel = idx
			t.redraw()
			return
		}
		if p.mouseSel == idx {
			t.selectPopup(idx)
		}
		p.mouseSel = -1
		return
	case cmdEnter:
		if len(p.opts) > 0 {
			t.selectPopup(p.sel)
		}
	case cmdScrollUp:
		p.move(-1)
		t.redraw()
	case cmdScrollDown:
		p.move(1)
		t.redraw()
	case cmdKey:
		for i, o := range p.opts {
			if o.key != 0 && ev.key == o.key {
				t.selectPopup(i)
				return
			}
		}
	case cmdQuit:
		if !p.escapable {
			return
		}
		if t.popupCancel != nil {
			t.popupCancel()
		}
		t.popup = nil
		t.popupOK = nil
		t.popupCancel = nil
		t.redraw()
	case cmdTick:
		if t.working {
			t.redraw()
		}
	}
}

// showApproval presents a human-in-the-loop checkpoint the agent reached:
// what is being asked plus Allow / Always allow / Deny, opencode-style.
func (t *chatTUI) showApproval(reply *ChatReply) {
	body := wrapLines(reply.Pending.Message, 56)
	opts := []popupOption{
		{label: "Allow", key: 'a', kind: "allow"},
		{label: "Always allow", key: 'y', kind: "allow"},
		{label: "Deny", key: 'd', kind: "deny"},
	}
	t.showPopup("approval required", body, opts, func(idx int) {
		verdict := [...]string{"allowed", "always allowed", "denied"}[clamp(idx, 0, 2)]
		t.blocks = append(t.blocks, block{role: "system", text: verdict + " · " + reply.Pending.Message})
	}, func() {
		t.blocks = append(t.blocks, block{role: "system", text: "denied · " + reply.Pending.Message})
	})
}

// showCommandPopup presents the slash commands as an autocomplete palette.
// Choosing a command inserts it into the input; the user still presses Enter
// to execute it, keeping the palette from unexpectedly running commands.
func (t *chatTUI) showCommandPopup() {
	commands := []struct {
		name string
		help string
	}{
		{"/model", "switch the active model"},
		{"/compact", "summarize and refresh the conversation"},
		{"/branch-timeline", "inspect and select a branch point"},
		{"/help", "show keyboard shortcuts"},
		{"/quit", "leave the harness"},
		{"/exit", "leave the harness"},
	}
	opts := make([]popupOption, 0, len(commands))
	for _, command := range commands {
		opts = append(opts, popupOption{label: command.name + "  " + command.help})
	}
	t.showPopup("commands", []string{"choose a command"}, opts, func(idx int) {
		t.input = commands[idx].name
	}, func() {})
}

// ---- launch ----

// beginLaunch resumes the requested session or opens the session/model popups.
func (t *chatTUI) beginLaunch(sessions []store.SessionMeta, opts RunOptions) {
	if opts.SessionID != uuid.Nil {
		t.resumeSession(opts.SessionID)
		return
	}
	if len(sessions) == 0 {
		t.pickModelPopup(false, func() { t.quitNow = true })
		return
	}
	optsList := []popupOption{{label: "Start a new session", key: 'n', kind: "new"}}
	for i, m := range sessions {
		label := fmt.Sprintf("%s · %s · %s", shortID(m.ID), orModel(m.Model), humanTime(m.UpdatedAt))
		if i < 9 {
			optsList = append(optsList, popupOption{label: label, key: '1' + rune(i)})
		} else {
			optsList = append(optsList, popupOption{label: label})
		}
	}
	t.showPopupMode("infai · choose a session",
		[]string{"resume a conversation or start a fresh one"},
		optsList,
		false,
		func(idx int) {
			if idx == 0 {
				t.pickModelPopup(false, func() { t.quitNow = true })
				return
			}
			t.resumeSession(sessions[idx-1].ID)
		},
		func() { t.quitNow = true })
}

// resumeSession loads a saved session into the TUI and renders its history.
func (t *chatTUI) resumeSession(id uuid.UUID) {
	meta, err := t.client.LoadSession(t.ctx, id)
	if err != nil {
		t.showPopup("could not load session", wrapLines(err.Error(), 56),
			[]popupOption{{label: "OK", key: 'o'}}, func(int) {}, func() { t.quitNow = true })
		return
	}
	_, records, err := t.client.GetSession(t.ctx, id)
	if err != nil {
		t.showPopup("could not load session", wrapLines(err.Error(), 56),
			[]popupOption{{label: "OK", key: 'o'}}, func(int) {}, func() { t.quitNow = true })
		return
	}
	t.session = *meta
	t.client.SetSession(meta.ID)
	t.blocks = blocksFromRecords(records)
}

// providerModel is a (provider, model) pair selectable from the model popup.
type providerModel struct{ provider, model string }

// pickModelPopup opens the model picker. switchModel is true for /model (the
// active session switches to the choice) and false at launch (a new session is
// created). cancel runs when the user dismisses the popup with Esc.
func (t *chatTUI) pickModelPopup(switchModel bool, cancel func()) {
	providers, err := t.client.ListProviders(t.ctx)
	if err != nil {
		t.showPopup("could not list models", wrapLines(err.Error(), 56),
			[]popupOption{{label: "OK", key: 'o'}}, func(int) {}, func() { t.quitNow = true })
		return
	}
	var pairs []providerModel
	var opts []popupOption
	for _, p := range providers {
		for _, name := range p.ModelNames() {
			pairs = append(pairs, providerModel{p.Name, name})
			opts = append(opts, popupOption{label: fmt.Sprintf("%s @ %s", name, p.Name)})
		}
	}
	if len(opts) == 0 {
		t.showPopup("no models configured", []string{"add providers/models to models.json and restart the server"},
			[]popupOption{{label: "OK", key: 'o'}}, func(int) {}, func() { t.quitNow = true })
		return
	}
	t.showPopup("choose a model", []string{"select the model to chat with"}, opts,
		func(idx int) {
			pair := pairs[idx]
			if switchModel {
				if err := t.client.SetSessionModel(t.ctx, pair.provider, pair.model); err != nil {
					t.appendError(err)
					return
				}
				t.session.Provider = pair.provider
				t.session.Model = pair.model
				return
			}
			t.createSession(pair.provider, pair.model)
		},
		cancel)
}

// createSession creates a fresh session for the chosen provider/model.
func (t *chatTUI) createSession(provider, model string) {
	cwd, _ := os.Getwd()
	meta, err := t.client.CreateSession(t.ctx, SessionCreateOptions{Provider: provider, Model: model, Cwd: cwd})
	if err != nil {
		t.showPopup("could not create session", wrapLines(err.Error(), 56),
			[]popupOption{{label: "OK", key: 'o'}}, func(int) {}, func() { t.quitNow = true })
		return
	}
	t.session = *meta
	t.client.SetSession(meta.ID)
}

// runTUICommand handles a slash command typed into the chat input.
func (t *chatTUI) runTUICommand(line string) {
	switch line {
	case "/model":
		t.pickModelPopup(true, func() {}) // switch the active session's model
	case "/compact":
		t.compactSession()
	case "/branch-timeline":
		t.showBranchTimeline()
	case "/quit", "/exit":
		t.quitNow = true
	case "/help":
		t.blocks = append(t.blocks, block{role: "system", text: strings.Join([]string{
			"/model · switch model via popup",
			"/quit, /exit · leave",
			"enter submits · ctrl+j / alt+enter newline · wheel or arrows scroll",
		}, "\n")})
	default:
		t.appendError(fmt.Errorf("unknown command %q — /help for options", line))
	}
	t.redraw()
}

func (t *chatTUI) compactSession() {
	meta, err := t.client.Compact(t.ctx)
	if err != nil {
		t.appendError(err)
		return
	}
	_, records, err := t.client.GetSession(t.ctx, meta.ID)
	if err != nil {
		t.appendError(err)
		return
	}
	t.session = *meta
	t.used = 0
	t.blocks = blocksFromRecords(records)
}

func (t *chatTUI) showBranchTimeline() {
	view, err := t.client.GetTimeline(t.ctx, t.session.ID)
	if err != nil {
		t.appendError(err)
		t.redraw()
		return
	}
	parents := make(map[uuid.UUID]uuid.UUID, len(view.Events))
	byID := make(map[uuid.UUID]TimelineEvent, len(view.Events))
	for _, event := range view.Events {
		parents[event.ID] = event.ParentID
		byID[event.ID] = event
	}
	events := timelineTreeOrder(view.Events)
	if len(events) == 0 {
		t.appendError(fmt.Errorf("timeline is empty"))
		t.redraw()
		return
	}
	options := make([]popupOption, 0, len(events))
	for _, event := range events {
		depth := 0
		cursor := event
		for cursor.ParentID != uuid.Nil && depth < 32 {
			if cursor.BranchFrom != nil {
				depth++
			}
			cursor = byID[parents[cursor.ID]]
		}
		options = append(options, popupOption{
			prefix: timelineTreePrefix(event, parents, byID),
			label:  timelineEventLabel(event),
			style:  timelineEventRole(event),
		})
	}
	t.showPopup("branch timeline", []string{"select where the next prompt should branch"}, options, func(idx int) {
		selected := events[idx]
		if err := t.client.SelectBranch(t.ctx, t.session.ID, selected.ID); err != nil {
			t.appendError(err)
			return
		}
		t.blocks = append(t.blocks, block{role: "system", text: branchSelectionLabel(selected)})
	}, func() {})
}

// timelineTreeOrder lays out the event graph as a tree instead of displaying
// events in UUID/index order. Branch roots are visited before the original
// continuation so a branch appears immediately below its branch_from event.
func timelineTreeOrder(events []TimelineEvent) []TimelineEvent {
	children := make(map[uuid.UUID][]TimelineEvent, len(events))
	for _, event := range events {
		children[event.ParentID] = append(children[event.ParentID], event)
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			a, b := children[parent][i], children[parent][j]
			if (a.BranchFrom != nil) != (b.BranchFrom != nil) {
				return a.BranchFrom != nil
			}
			return a.ID.String() < b.ID.String()
		})
	}

	ordered := make([]TimelineEvent, 0, len(events))
	seen := make(map[uuid.UUID]struct{}, len(events))
	var visit func(uuid.UUID)
	visit = func(parent uuid.UUID) {
		for _, event := range children[parent] {
			if _, ok := seen[event.ID]; ok {
				continue
			}
			seen[event.ID] = struct{}{}
			ordered = append(ordered, event)
			visit(event.ID)
		}
	}
	visit(uuid.Nil)

	// Keep malformed/disconnected events visible rather than silently dropping
	// them from the branch picker.
	if len(ordered) != len(events) {
		remaining := make([]TimelineEvent, 0, len(events)-len(ordered))
		for _, event := range events {
			if _, ok := seen[event.ID]; !ok {
				remaining = append(remaining, event)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return remaining[i].ID.String() < remaining[j].ID.String() })
		ordered = append(ordered, remaining...)
	}
	return ordered
}

func timelineTreePrefix(event TimelineEvent, parents map[uuid.UUID]uuid.UUID, byID map[uuid.UUID]TimelineEvent) string {
	depth := 0
	cursor := event
	for cursor.ParentID != uuid.Nil && depth < 32 {
		if cursor.BranchFrom != nil {
			depth++
		}
		cursor = byID[parents[cursor.ID]]
	}
	if depth == 0 {
		return ""
	}
	if event.BranchFrom != nil {
		return strings.Repeat("│  ", depth-1) + "└─ "
	}
	return strings.Repeat("│  ", depth)
}

func timelineEventLabel(event TimelineEvent) string {
	if event.Record == nil {
		return string(event.Kind) + "  blob content unavailable"
	}
	label := string(event.Record.Kind)
	text := event.Record.Text
	if event.Record.Message != nil {
		label = event.Record.Message.Role
		text = event.Record.Message.Text()
	}
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 48 {
		text = text[:48] + "..."
	}
	if text == "" {
		text = string(event.Record.Kind)
	}
	return label + "  " + text
}

func branchSelectionLabel(event TimelineEvent) string {
	preview := string(event.Kind)
	if event.Record != nil {
		if event.Record.Message != nil {
			preview = event.Record.Message.Text()
		} else if event.Record.Text != "" {
			preview = event.Record.Text
		}
	}
	preview = strings.ReplaceAll(preview, "\n", " ")
	if len(preview) > 72 {
		preview = preview[:72] + "..."
	}
	return fmt.Sprintf("⎇ branch selected at %s · %q", event.ID, preview)
}

func timelineEventRole(event TimelineEvent) string {
	if event.Record != nil && event.Record.Message != nil {
		switch event.Record.Message.Role {
		case "system", "user", "assistant":
			return event.Record.Message.Role
		}
	}
	if event.Kind == store.KindCompaction {
		return "system"
	}
	return ""
}

func (t *chatTUI) appendDelta(kind contracts.DeltaKind, text string) {
	role := "assistant"
	if kind == contracts.DeltaReasoning {
		role = "thinking"
	}
	if len(t.blocks) > 0 && t.blocks[len(t.blocks)-1].role == role {
		t.blocks[len(t.blocks)-1].text += text
		return
	}
	t.blocks = append(t.blocks, block{role: role, text: text})
}

func (t *chatTUI) appendStatus(text string) {
	text = statusLabel(text)
	if len(t.blocks) > 0 && t.blocks[len(t.blocks)-1].role == "status" {
		t.blocks[len(t.blocks)-1].text = text
		return
	}
	t.blocks = append(t.blocks, block{role: "status", text: text})
}

func (t *chatTUI) appendCompactionSummary(summary string) {
	t.blocks = append(t.blocks, block{role: "compaction", text: summary})
}

func compactionBoundary(width int) string {
	label := " context boundary "
	if width <= len(label) {
		return label[:width]
	}
	left := (width - len(label)) / 2
	right := width - len(label) - left
	return strings.Repeat("┄", left) + label + strings.Repeat("┄", right)
}

func (t *chatTUI) appendError(err error) {
	if len(t.blocks) > 0 && t.blocks[len(t.blocks)-1].role == "error" {
		t.blocks[len(t.blocks)-1].text = err.Error()
		return
	}
	t.blocks = append(t.blocks, block{role: "error", text: err.Error()})
}

// blocksFromRecords converts saved timeline records into chat blocks so a resumed
// session renders its history.
func blocksFromRecords(records []store.Record) []block {
	var blocks []block
	for _, rec := range records {
		if rec.Kind == store.KindCompaction && rec.Compaction != nil {
			blocks = append(blocks, block{role: "compaction", text: rec.Compaction.Summary})
			continue
		}
		if rec.Kind != store.KindMessage || rec.Message == nil {
			continue
		}
		m := rec.Message
		switch m.Role {
		case "user":
			blocks = append(blocks, block{role: "user", text: m.Text()})
		case "assistant":
			if m.ReasoningContent != "" {
				blocks = append(blocks, block{role: "thinking", text: m.ReasoningContent})
			}
			blocks = append(blocks, block{role: "assistant", text: m.Text()})
		}
	}
	return blocks
}

// readKeys pushes decoded terminal input onto t.events.
func (t *chatTUI) readKeys() {
	r := bufio.NewReader(os.Stdin)
	for {
		c, err := r.ReadByte()
		if err != nil {
			t.events <- uiEvent{cmd: cmdQuit}
			return
		}

		// While bracketed paste is active, everything is literal: newlines and
		// Enter stay as text, only ESC[201~ ends the paste. UTF-8 is decoded
		// rune by rune so multi-byte characters of any width survive intact.
		if t.pasting {
			if c == 0x1b {
				if next, err := r.Peek(5); err == nil && string(next) == "[201~" {
					_, _ = r.Discard(5)
					t.pasting = false
					t.events <- uiEvent{cmd: cmdPaste, paste: t.pasteBuf}
					t.pasteBuf = ""
					continue
				}
			}
			seq := []byte{c}
			for !utf8.FullRune(seq) {
				b, err := r.ReadByte()
				if err != nil {
					break
				}
				if b == 0x1b { // paste-end starting mid-rune; handle next loop
					break
				}
				seq = append(seq, b)
			}
			if run, _ := utf8.DecodeRune(seq); run != utf8.RuneError {
				t.pasteBuf += string(run)
			}
			continue
		}

		switch c {
		case 0x1b: // escape sequence, Alt+Enter, or a mouse report
			if r.Buffered() == 0 && !waitForInput(int(os.Stdin.Fd()), 50*time.Millisecond) {
				t.events <- uiEvent{cmd: cmdQuit}
				continue
			}
			next, err := r.Peek(1)
			if err != nil {
				t.events <- uiEvent{cmd: cmdQuit}
				continue
			}
			switch next[0] {
			case '[': // CSI or X10/SGR mouse
				_, _ = r.Discard(1) // consume '['
				if b, err := r.Peek(1); err == nil {
					switch b[0] {
					case '<': // SGR mouse report: ESC [ < btn ; x ; y M|m
						_, _ = r.Discard(1)
						if btn, x, y, press := sgrMouse(r); btn >= 0 {
							t.events <- t.mouseEvent(btn, x, y, press)
						}
						continue
					case 'M': // X10 mouse report: ESC [ M <btn+32> <x+32> <y+32>
						_, _ = r.Discard(1)
						if b1, b2, b3, err := read3(r); err == nil {
							t.events <- t.mouseEvent(int(b1)-32, int(b2)-32, int(b3)-32, true)
						}
						continue
					}
				}
				var seq []byte
				for {
					b, err := r.ReadByte()
					if err != nil {
						break
					}
					seq = append(seq, b)
					if b >= 0x40 && b <= 0x7e {
						break
					}
				}
				if string(seq) == "200~" {
					t.pasting = true
					t.pasteBuf = ""
					continue
				}
				t.handleCSI(seq)
			case '<': // SGR mouse report (some terminals skip the '[')
				_, _ = r.Discard(1)
				if btn, x, y, press := sgrMouse(r); btn >= 0 {
					t.events <- t.mouseEvent(btn, x, y, press)
				}
			case 'M': // X10 mouse report without the '['
				_, _ = r.Discard(1)
				if b1, b2, b3, err := read3(r); err == nil {
					t.events <- t.mouseEvent(int(b1)-32, int(b2)-32, int(b3)-32, true)
				}
			case 0x0d: // Alt+Enter = newline
				_, _ = r.Discard(1)
				t.events <- uiEvent{cmd: cmdNewline}
			}
		case 0x0d:
			t.events <- uiEvent{cmd: cmdEnter}
		case 0x0a: // Ctrl+J = newline
			t.events <- uiEvent{cmd: cmdNewline}
		case 0x7f, 0x08:
			t.events <- uiEvent{cmd: cmdBackspace}
		case 0x03, 0x04:
			t.events <- uiEvent{cmd: cmdQuit}
		default:
			seq := []byte{c}
			for !utf8.FullRune(seq) {
				b, err := r.ReadByte()
				if err != nil {
					break
				}
				seq = append(seq, b)
			}
			run, _ := utf8.DecodeRune(seq)
			t.events <- uiEvent{cmd: cmdKey, key: run}
		}
	}
}

func waitForInput(fd int, timeout time.Duration) bool {
	if fd < 0 {
		return false
	}
	var set syscall.FdSet
	set.Bits[fd/64] |= 1 << uint(fd%64)
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	ready, err := syscall.Select(fd+1, &set, nil, nil, &tv)
	return err == nil && ready > 0
}

// CopyToClipboard copies text to the system clipboard via the first available
// of wl-copy / xclip / pbcopy.
func CopyToClipboard(text string) (string, error) {
	for _, entry := range []struct {
		bin  string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"pbcopy", nil},
	} {
		path, err := exec.LookPath(entry.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, entry.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return entry.bin, err
		}
		return entry.bin, nil
	}
	return "", nil
}

// sgrMouse reads an SGR mouse report ("ESC[<btn;x;yM") and returns the button
// code, cell coordinates (1-based) and whether it was a press ('M') rather
// than a release ('m'). btn is -1 on a malformed report. Wheel up is 64, wheel
// down is 65, left drag is 32.
func sgrMouse(r *bufio.Reader) (btn, x, y int, press bool) {
	btn, x, y = 0, 0, 0
	press = true
	part := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return -1, 0, 0, false
		}
		switch {
		case b == ';':
			part++
		case b == 'M':
			return btn, x, y, true
		case b == 'm':
			return btn, x, y, false
		case b >= '0' && b <= '9':
			switch part {
			case 0:
				btn = btn*10 + int(b-'0')
			case 1:
				x = x*10 + int(b-'0')
			case 2:
				y = y*10 + int(b-'0')
			}
		}
	}
}

// read3 reads three consecutive bytes (for X10 mouse reports).
func read3(r *bufio.Reader) (byte, byte, byte, error) {
	b1, err := r.ReadByte()
	if err != nil {
		return 0, 0, 0, err
	}
	b2, err := r.ReadByte()
	if err != nil {
		return 0, 0, 0, err
	}
	b3, err := r.ReadByte()
	return b1, b2, b3, err
}

// mouseEvent routes a mouse report to scroll or selection.
func (t *chatTUI) mouseEvent(btn, x, y int, press bool) uiEvent {
	switch btn {
	case 64: // wheel up
		return uiEvent{cmd: cmdScrollUp}
	case 65: // wheel down
		return uiEvent{cmd: cmdScrollDown}
	}
	// left press/drag (0/32) and everything else feed the selection handler
	return uiEvent{cmd: cmdMouse, btn: btn, x: x, y: y, press: press}
}

// handleMouse updates the app-level selection from a mouse event.
func (t *chatTUI) handleMouse(ev uiEvent) {
	if ev.press {
		if p, ok := t.posFromMouse(ev.x, ev.y); ok {
			if ev.btn == 0 { // left down: start
				t.selecting = true
				t.selStart, t.selEnd = p, p
			} else { // drag: extend
				t.selEnd = p
			}
		} else {
			t.selecting = false
		}
		return
	}
	// release
	if t.selecting {
		t.copySelection()
		t.selecting = false
	}
}

// posFromMouse maps a mouse cell (1-based) to a content position, if it lands
// in the history region.
func (t *chatTUI) posFromMouse(x, y int) (pos, bool) {
	avail := t.historyHeight()
	row := y - 1 // history occupies the top rows
	if row < 0 || row >= avail {
		return pos{}, false
	}
	start := max(len(t.rows)-avail-t.scroll, 0)
	contentRow := start + row
	if contentRow < 0 || contentRow >= len(t.rows) {
		return pos{}, false
	}
	col := max(x-1, 0)
	if n := utf8.RuneCountInString(t.rows[contentRow].plain); col > n {
		col = n
	}
	return pos{contentRow, col}, true
}

// selBounds returns the normalized [start,end] content positions.
func (t *chatTUI) selBounds() (pos, pos) {
	a, b := t.selStart, t.selEnd
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		return b, a
	}
	return a, b
}

// renderRow colors a row's plain text, wrapping any selected columns in
// reverse video.
func (t *chatTUI) renderRow(hr histRow, selected bool, a, b int) string {
	if hr.style == "user" || hr.style == "assistant" {
		return renderChatRow(hr.plain, hr.prefixLen, selected, a, b, hr.style)
	}
	s := hr.plain
	if selected {
		runes := []rune(s)
		lo := clamp(a, 0, len(runes))
		hi := clamp(b, 0, len(runes))
		if lo < hi {
			s = string(runes[:lo]) + "\033[7m" + string(runes[lo:hi]) + "\033[0m" + string(runes[hi:])
		}
	}
	switch hr.style {
	case "user":
		return cUser.Sprint(s)
	case "assistant":
		return cAssistant.Sprint(s)
	case "thinking":
		return cThinking.Sprint(s)
	case "error":
		return cError.Sprint(s)
	case "system":
		return cSystem.Sprint(s)
	case "compaction":
		return cThinking.Sprint(s)
	case "status":
		return cSystem.Sprint(s)
	}
	return s
}

func renderChatRow(text string, prefixLen int, selected bool, a, b int, role string) string {
	runes := []rune(text)
	prefixLen = clamp(prefixLen, 0, len(runes))
	dotStyle := cUser
	if role == "assistant" {
		dotStyle = cAssistant
	}
	if !selected {
		return dotStyle.Sprint(string(runes[:prefixLen])) + cChatText.Sprint(string(runes[prefixLen:]))
	}
	return renderSelectedSegment(string(runes[:prefixLen]), dotStyle, 0, a, b) +
		renderSelectedSegment(string(runes[prefixLen:]), cChatText, prefixLen, a, b)
}

func renderSelectedSegment(text string, style *color.Color, offset, start, end int) string {
	runes := []rune(text)
	lo := clamp(start-offset, 0, len(runes))
	hi := clamp(end-offset, 0, len(runes))
	if lo >= hi {
		return style.Sprint(text)
	}
	return style.Sprint(string(runes[:lo])) + "\033[7m" + style.Sprint(string(runes[lo:hi])) + "\033[0m" + style.Sprint(string(runes[hi:]))
}

// copySelection copies the selected text to the clipboard.
func (t *chatTUI) copySelection() {
	if !t.selecting {
		return
	}
	lo, hi := t.selBounds()
	if lo.row >= len(t.rows) {
		return
	}
	var parts []string
	for r := lo.row; r <= hi.row && r < len(t.rows); r++ {
		line := t.rows[r].plain
		if r == lo.row && r == hi.row {
			parts = append(parts, sliceRunes(line, lo.col, hi.col))
		} else if r == lo.row {
			parts = append(parts, sliceRunes(line, lo.col, utf8.RuneCountInString(line)))
		} else if r == hi.row {
			parts = append(parts, sliceRunes(line, 0, hi.col))
		} else {
			parts = append(parts, line)
		}
	}
	if _, err := CopyToClipboard(strings.Join(parts, "\n")); err != nil {
		t.flash = "copy failed: " + err.Error()
	} else {
		t.flash = "copied"
	}
}

func sliceRunes(s string, a, b int) string {
	r := []rune(s)
	if a < 0 {
		a = 0
	}
	if b > len(r) {
		b = len(r)
	}
	if a > b {
		a = b
	}
	return string(r[a:b])
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// handleCSI maps arrow, page and modified-Enter sequences to actions.
func (t *chatTUI) handleCSI(seq []byte) {
	if len(seq) == 0 {
		return
	}
	if modifiedEnter(seq) {
		t.events <- uiEvent{cmd: cmdNewline}
		return
	}
	switch seq[len(seq)-1] {
	case 'A':
		t.events <- uiEvent{cmd: cmdScrollUp}
	case 'B':
		t.events <- uiEvent{cmd: cmdScrollDown}
	case '~':
		if len(seq) >= 2 {
			switch seq[0] {
			case '5':
				t.events <- uiEvent{cmd: cmdScrollUp, page: true}
			case '6':
				t.events <- uiEvent{cmd: cmdScrollDown, page: true}
			}
		}
	}
}

// modifiedEnter reports whether a CSI sequence is Enter with a modifier, which
// terminals send for Shift+Enter / Alt+Enter instead of a raw "\x1b\r". These
// are the newline-in-input key in Kitty ("13;2u"/"13;3u") and xterm
// modifyOtherKeys ("27;2;13~"/"27;3;13~").
func modifiedEnter(seq []byte) bool {
	s := string(seq)
	var key, mod int
	if before, ok := strings.CutSuffix(s, "u"); ok {
		parts := strings.Split(before, ";")
		if len(parts) < 2 {
			return false
		}
		_, _ = fmt.Sscanf(parts[0], "%d", &key)
		_, _ = fmt.Sscanf(parts[1], "%d", &mod)
	} else if strings.HasSuffix(s, "~") && strings.HasPrefix(s, "27;") {
		parts := strings.Split(strings.TrimSuffix(s, "~"), ";")
		if len(parts) < 3 {
			return false
		}
		_, _ = fmt.Sscanf(parts[1], "%d", &mod)
		_, _ = fmt.Sscanf(parts[2], "%d", &key)
	}
	return key == 13 && (mod == 2 || mod == 3 || mod == 5)
}

// historyHeight is how many terminal rows the history region occupies.
func (t *chatTUI) historyHeight() int {
	avail := t.height - t.inputAreaRows() - t.statusRows() - t.brandRows()
	if avail < 0 {
		return 0
	}
	return avail
}

func (t *chatTUI) maxScroll() int {
	avail := t.historyHeight()
	h := len(t.buildRows())
	if h <= avail {
		return 0
	}
	return h - avail
}

func (t *chatTUI) scrollUp(page bool) {
	step := 3 // wheel feels like a few lines per tick
	if page {
		step = t.historyHeight()
	}
	t.selecting = false
	t.scroll += step
	if t.scroll > t.maxScroll() {
		t.scroll = t.maxScroll()
	}
}

func (t *chatTUI) scrollDown(page bool) {
	step := 3
	if page {
		step = t.historyHeight()
	}
	t.selecting = false
	t.scroll -= step
	if t.scroll < 0 {
		t.scroll = 0
	}
}

// tick drives the spinner animation while working, and stops on exit.
func (t *chatTUI) tick() {
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			t.events <- uiEvent{cmd: cmdTick}
		}
	}
}

// ---- rendering ----

func (t *chatTUI) redraw() {
	var sb strings.Builder
	sb.WriteString("\033[H")

	// conversation history: exactly `avail` rows starting at `start`, so
	// scrolling reveals earlier rows and hides the bottom ones.
	t.rows = t.buildRows()
	avail := t.historyHeight()
	if t.scroll > t.maxScroll() {
		t.scroll = t.maxScroll()
	}
	start := max(len(t.rows)-avail-t.scroll, 0)
	lo, hi := pos{}, pos{}
	hasSel := t.selecting
	if hasSel {
		lo, hi = t.selBounds()
	}
	for i := range avail {
		r := start + i
		if r >= len(t.rows) {
			sb.WriteString("\033[K\r\n")
			continue
		}
		sel := false
		a, b := 0, 0
		if hasSel && r >= lo.row && r <= hi.row {
			sel = true
			a = lo.col
			b = hi.col
			if r != lo.row {
				a = 0
			}
			if r != hi.row {
				b = utf8.RuneCountInString(t.rows[r].plain)
			}
		}
		sb.WriteString(t.renderRow(t.rows[r], sel, a, b))
		sb.WriteString("\033[K\r\n")
	}

	// status bar
	status := t.statusLine()
	if t.flash != "" {
		status = cSystem.Sprintf("%s · %s", status, t.flash)
		t.flash = ""
	}
	sb.WriteString(status)
	sb.WriteString("\033[K\r\n")

	// input (sticky at the very bottom), framed by top and bottom rules, with
	// the "infai harness" brand line beneath it. On a short window the input
	// is clamped to its last lines so the prompt stays anchored.
	sb.WriteString(cHeader.Sprint(strings.Repeat("─", t.width)))
	sb.WriteString("\033[K\r\n")
	lines := strings.Split(t.input, "\n")
	vis := min(len(lines), t.inputRows())
	for i := len(lines) - vis; i < len(lines); i++ {
		if i == len(lines)-1 {
			sb.WriteString(cPrompt.Sprint("λ ") + cChatText.Sprint(lines[i]))
			sb.WriteString("\033[K")
		} else {
			sb.WriteString(lines[i])
			sb.WriteString("\033[K\r\n")
		}
	}
	sb.WriteString("\r\n")
	sb.WriteString(cHeader.Sprint(strings.Repeat("─", t.width)))
	sb.WriteString("\033[K\r\n")
	sb.WriteString(cHeader.Sprint("infai harness"))
	sb.WriteString("\033[K")

	// Floating popup overlay: hide the chat cursor and draw the box centered
	// over the history+status region (absolute positioning). Otherwise place
	// the blinking cursor back on the input's last line at its column.
	if t.popup != nil {
		t.drawPopup(&sb)
		sb.WriteString("\033[?25l")
	} else {
		fmt.Fprintf(&sb, "\033[2A\r\033[%dC", utf8.RuneCountInString(lines[len(lines)-1])+2)
		sb.WriteString("\033[?25h")
	}

	_, _ = io.WriteString(os.Stdout, sb.String())
}

// drawPopup renders the open popup as a centered overlay over the history and
// status region. It degrades to compact on short windows (body truncated,
// options scroll) and never overflows the terminal.
func (t *chatTUI) drawPopup(sb *strings.Builder) {
	p := t.popup
	if p == nil {
		return
	}
	boxW := min(96, t.width-4)
	if boxW < 8 {
		boxW = t.width - 2
	}
	if boxW < 8 {
		boxW = 8
	}
	regionH := t.historyHeight() + t.statusRows()
	maxH := max(4, regionH)
	lines := p.lines(boxW, maxH)
	boxH := len(lines)
	if boxH > maxH {
		boxH = maxH
		lines = lines[:boxH]
	}
	left := max((t.width-boxW)/2, 0)
	top := max((regionH-boxH)/2, 0)
	p.setGeometry(left, top, maxH)
	for i, l := range lines {
		fmt.Fprintf(sb, "\033[%d;%dH%s", top+1+i, left+1, l)
		sb.WriteString("\033[K")
	}
}

// inputRows is how many terminal rows the input text occupies, clamped so the
// input never crowds out the history region on a short window.
func (t *chatTUI) inputRows() int {
	n := len(strings.Split(t.input, "\n"))
	if cap := t.height - 5; n > cap { // status + 2 borders + brand = 4 fixed rows
		if cap < 1 {
			return 1
		}
		return cap
	}
	return n
}

// inputAreaRows is the input text plus its top/bottom border lines.
func (t *chatTUI) inputAreaRows() int {
	return t.inputRows() + 2
}

// statusRows is the height of the status bar; the working indicator (spinner)
// IS the status line, so it is always one row.
func (t *chatTUI) statusRows() int {
	return 1
}

// brandRows is the "infai harness" line below the input.
func (t *chatTUI) brandRows() int {
	return 1
}

// buildRows renders the conversation as plain (uncolored) wrapped rows tagged
// with their style. Plain text is kept so selection can map cells and extract
// the copied text; coloring happens in renderRow.
func (t *chatTUI) buildRows() []histRow {
	var out []histRow
	for _, b := range t.blocks {
		switch b.role {
		case "user":
			for i, l := range wrap(b.text, t.width-3) {
				prefix := "● "
				prefixLen := 2
				if i > 0 {
					prefix = "  "
					prefixLen = 0
				}
				out = append(out, histRow{plain: prefix + l, style: "user", prefixLen: prefixLen})
			}
		case "assistant":
			for i, l := range wrap(b.text, t.width-3) {
				prefix := "● "
				prefixLen := 2
				if i > 0 {
					prefix = "  "
					prefixLen = 0
				}
				out = append(out, histRow{plain: prefix + l, style: "assistant", prefixLen: prefixLen})
			}
		case "thinking":
			out = append(out, histRow{plain: "─ thinking ─", style: "thinking"})
			for _, l := range wrap(b.text, t.width) {
				out = append(out, histRow{plain: l, style: "thinking"})
			}
		case "error":
			for _, l := range wrap(b.text, t.width-3) {
				out = append(out, histRow{plain: "! " + l, style: "error"})
			}
		case "system":
			for _, l := range wrap(b.text, t.width-3) {
				out = append(out, histRow{plain: "· " + l, style: "system"})
			}
		case "compaction":
			out = append(out, histRow{plain: compactionBoundary(t.width), style: "compaction"})
			out = append(out, histRow{plain: "✓ compaction summary:", style: "compaction"})
			for _, l := range wrap(b.text, t.width-3) {
				out = append(out, histRow{plain: "  " + l, style: "compaction"})
			}
		}
	}
	return out
}

// spinnerFrame returns the current spinner glyph, driven by elapsed time.
func (t *chatTUI) spinnerFrame() string {
	frames := []string{"◐", "◓", "◑", "◒"}
	return frames[time.Since(t.workBegan).Milliseconds()/200%int64(len(frames))]
}

// statusLine builds the model/session/context status bar.
func (t *chatTUI) statusLine() string {
	if t.session.ID == uuid.Nil {
		return cHeader.Sprint("no session · pick one from the popup above")
	}
	scrolled := ""
	if t.scroll > 0 {
		scrolled = fmt.Sprintf("  ·  ↑ %d", t.scroll)
	}
	if t.working {
		pct := 0
		if t.session.ContextWindow > 0 {
			pct = t.used * 100 / t.session.ContextWindow
		}
		return cThinking.Sprintf("%s %s · %s · ctx: %d/%d (%d%%)",
			t.spinnerFrame(), t.session.Model, time.Since(t.workBegan).Round(time.Second), t.used, t.session.ContextWindow, pct)
	}
	pct := 0
	if t.session.ContextWindow > 0 {
		pct = t.used * 100 / t.session.ContextWindow
	}
	return cHeader.Sprintf("model: %s  ·  ctx: %d/%d (%d%%)  ·  sess: %s  ·  turns: %d%s",
		t.session.Model, t.used, t.session.ContextWindow, pct, shortID(t.session.ID), t.session.TurnCount, scrolled)
}

// wrap splits long lines to at most w runes (char-based; colored per line by
// the caller).
func wrap(s string, w int) []string {
	if w <= 0 {
		w = 80
	}
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		runes := []rune(line)
		for len(runes) > w {
			out = append(out, string(runes[:w]))
			runes = runes[w:]
		}
		out = append(out, string(runes))
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
