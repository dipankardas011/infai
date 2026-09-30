package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/aymanbagabas/go-udiff"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/agent/vision"
	"github.com/google/uuid"
	"github.com/sergi/go-diff/diffmatchpatch"
)

type block struct {
	role          string
	text          string
	imageCount    int
	toolKind      string
	toolStatus    string
	toolName      string
	toolArgs      string
	rendered      string
	renderedWidth int
	renderedLines int
	renderedValid bool
}

type chatModel struct {
	ctx    context.Context
	client Client
	styles harnessStyles

	session           store.SessionMeta
	contextWindow     uint64
	thinking          contracts.InfaiThinkingLevel
	availableThinking []contracts.InfaiThinkingLevel
	modalities        []contracts.LLMSupportedModality
	pending           []contracts.ImageInput
	clipboard         ClipboardReader
	used              uint64
	blocks            []block

	width            int
	height           int
	areas            []rowArea
	viewport         viewport.Model
	anchor           transcriptAnchor
	composer         textarea.Model
	checklist        contracts.TaskChecklistState
	status           contracts.SessionStatus
	approval         *Approval
	approvalShown    bool
	composerWaiting  bool
	approvalReason   bool
	approvalDraft    string
	tailKey          tailKey
	tailRendered     string
	tailLines        int
	tailValid        bool
	modal            *modalModel
	commandMenu      bool
	commandSelection int
	filePicker       *filePicker

	working           bool
	workBegan         time.Time
	workStatus        string
	cancelArmed       bool
	cancelArmID       uint64
	cancelStatus      string
	sessionCancel     context.CancelFunc
	sessionStream     chan tea.Msg
	sessionObserverID uint64
	initCmd           tea.Cmd
	streaming         bool
	streamingAt       int
	toolCallNames     map[string]string
	skillNames        map[string]string
}

type sessionViewMsg struct {
	sessionID  uuid.UUID
	observerID uint64
	view       glue.SessionView
}

type sessionEventMsg struct {
	sessionID  uuid.UUID
	observerID uint64
	event      contracts.EventStream
}

type sessionJoinDoneMsg struct {
	sessionID  uuid.UUID
	observerID uint64
	err        error
}

type messageSentMsg struct {
	ownsWork bool
	err      error
}
type turnCanceledMsg struct{ err error }

type clipboardImageMsg struct {
	image contracts.ImageInput
	err   error
}

type editorDoneMsg struct {
	text string
	err  error
}

type sessionLoadedMsg struct {
	output  *glue.SessionOutput
	records []store.Record
	err     error
}
type sessionsListedMsg struct {
	sessions []contracts.SessionSummary
	err      error
}
type providersListedMsg struct {
	providers []glue.ListModelOutput
	switching bool
	err       error
}
type sessionCreatedMsg struct {
	output *glue.SessionOutput
	err    error
}
type modelSetMsg struct {
	output *glue.SessionOutput
	err    error
}
type compactedMsg struct {
	meta    *store.SessionMeta
	records []store.Record
	err     error
}
type timelineLoadedMsg struct {
	view *TimelineView
	err  error
}
type branchSelectedMsg struct {
	event     TimelineEvent
	checklist contracts.TaskChecklistState
	err       error
}
type approvalResolvedMsg struct{ err error }
type renamedMsg struct {
	meta *store.SessionMeta
	err  error
}
type animationTickMsg struct{}
type cancelArmTimeoutMsg struct{ id uint64 }

const cancelArmTimeout = 10 * time.Second

func runChatTUI(ctx context.Context, client Client, sessions []contracts.SessionSummary, opts RunOptions, in io.Reader, out io.Writer) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := newChatModel(runCtx, client, sessions, opts)
	_, err := tea.NewProgram(model,
		tea.WithContext(runCtx),
		tea.WithInput(in),
		tea.WithOutput(out),
	).Run()
	return err
}

func newChatModel(ctx context.Context, client Client, sessions []contracts.SessionSummary, opts RunOptions) *chatModel {
	input := textarea.New()
	input.Placeholder = "Ask, plan, build... (external editor ctrl+x)"
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.MaxContentHeight = 200
	input.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	input.SetVirtualCursor(true)
	styleTextarea(&input, false)

	// The viewport only renders the window it is handed: the model owns the
	// scroll position, soft wrapping is done per block before it gets there,
	// and the wheel is handled with the rest of the transcript input.
	view := viewport.New()
	view.SoftWrap = false
	view.FillHeight = true
	view.Style = lipgloss.NewStyle().Padding(0, 1)

	m := &chatModel{
		ctx:           ctx,
		client:        client,
		styles:        newHarnessStyles(),
		viewport:      view,
		composer:      input,
		clipboard:     defaultClipboard(),
		toolCallNames: make(map[string]string),
		skillNames:    make(map[string]string),
	}
	if opts.SessionID != uuid.Nil {
		m.modal = loadingModal("Opening session")
		m.initCmd = loadSessionCmd(ctx, client, opts.SessionID)
	} else {
		m.showSessions(sessions, true)
	}
	m.refreshInputMark()
	return m
}

// inputMark opens the composer: "∞" while it takes a prompt, "why" while it is
// capturing the reason a decision was denied.
const inputMark = "∞ "

// transcriptScrollStep is how far a wheel notch or ctrl+up/down moves the
// transcript, in lines. It matches what the viewport used to scroll by.
const transcriptScrollStep = 3

// refreshInputMark names what the composer is currently for, and pads the
// continuation rows of a wrapped draft to the same width. The placeholder spells
// out how to finish a reason, so the reserved block above does not have to.
func (m *chatModel) refreshInputMark() {
	// A pending decision owns the keyboard, so the composer is not taking a
	// prompt. Capturing a reason is the exception: the decision asked for it.
	waiting := m.approval != nil && !m.approvalReason
	mark := inputMark
	placeholder := "Ask, plan, build... (external editor ctrl+x)"
	switch {
	case m.approvalReason:
		mark = "why▸ "
		placeholder = "(reason to deny · ⏎ send · esc cancel)"
	case waiting:
		placeholder = "answer the decision above to continue"
	}
	if waiting != m.composerWaiting {
		m.composerWaiting = waiting
		styleTextarea(&m.composer, waiting)
	}
	m.composer.Placeholder = placeholder
	width := lipgloss.Width(mark)
	m.composer.SetPromptFunc(width, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return mark
		}
		return strings.Repeat(" ", width)
	})
}

func (m *chatModel) Init() tea.Cmd {
	return tea.Batch(m.composer.Focus(), m.initCmd)
}

func (m *chatModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A resize reflows the blocks around the anchor, so a reader stays on
		// the block they were reading; only a view that was already at the
		// newest output follows it.
		m.reflow()
		return m, nil
	case sessionViewMsg:
		if msg.sessionID != m.session.ID || msg.observerID != m.sessionObserverID {
			return m, nil
		}
		m.applySessionView(msg.view)
		m.refreshTranscript(true)
		m.reflow()
		return m, waitStream(m.ctx, m.sessionStream)
	case sessionEventMsg:
		if msg.sessionID != m.session.ID || msg.observerID != m.sessionObserverID {
			return m, nil
		}
		// Live output follows the newest line only for a reader who is already
		// there. Reading back through the transcript while a turn runs is what
		// scrolling is for, so an event must not pull the view down from under
		// them. Asked before the event lands: an event that adds a line moves
		// the newest line, and afterwards the answer describes the transcript
		// as it stands rather than the view the reader left.
		follow := m.atBottom()
		// Every event draws as it arrives. A refresh costs one block render
		// rather than the whole message, because the block in flight is cached
		// like every other one.
		m.applySessionEvent(msg.event)
		m.refreshTranscript(follow)
		m.reflow()
		if msg.event.Kind == contracts.EventSubscriberGap {
			return m, m.startSessionObserver(msg.sessionID)
		}
		return m, waitStream(m.ctx, m.sessionStream)
	case sessionJoinDoneMsg:
		if msg.sessionID == m.session.ID && msg.observerID == m.sessionObserverID && msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.appendError(msg.err)
			m.refreshTranscript(true)
		}
		return m, nil
	case messageSentMsg:
		if msg.err != nil {
			// Only a send that opened the turn may close it; a refused queued
			// prompt leaves the running turn's status alone.
			if msg.ownsWork {
				m.status = contracts.SessionIdle
				m.working = false
				m.workStatus = ""
			}
			m.appendError(msg.err)
			m.refreshTranscript(true)
			m.reflow()
			if msg.ownsWork && m.session.ID != uuid.Nil {
				return m, m.startSessionObserver(m.session.ID)
			}
		}
		return m, nil
	case turnCanceledMsg:
		// A cancel the server refused leaves the turn as it was; clearing the
		// label lets the status view fall back to working until the session
		// reports its own status again.
		if msg.err != nil {
			m.workStatus = ""
			m.appendError(msg.err)
			m.refreshTranscript(true)
		}
		return m, nil
	case clipboardImageMsg:
		return m.handleClipboardImage(msg)
	case editorDoneMsg:
		if msg.err != nil {
			m.appendError(fmt.Errorf("open editor: %w", msg.err))
			m.refreshTranscript(true)
			m.reflow()
			return m, nil
		}
		m.composer.SetValue(msg.text)
		m.composer.CursorEnd()
		m.updateCommandMenu()
		m.reflow()
		return m, nil
	case sessionLoadedMsg:
		if msg.err != nil {
			m.showNotice("Could not open session", msg.err.Error(), true)
			return m, nil
		}
		m.session = msg.output.SessionMeta
		m.contextWindow = msg.output.ContextWindow
		m.thinking = msg.output.Thinking
		m.availableThinking = msg.output.AvailableThinking
		m.modalities = msg.output.Modalities
		m.pending = nil
		m.reflow()
		m.client.SetSession(msg.output.ID)
		m.blocks = blocksFromRecords(msg.records)
		m.checklist = taskChecklistFromRecords(msg.records)
		m.modal = nil
		m.refreshTranscript(true)
		return m, m.startSessionObserver(msg.output.ID)
	case sessionsListedMsg:
		if msg.err != nil {
			m.showNotice("Could not list sessions", msg.err.Error(), false)
		} else {
			m.showSessions(msg.sessions, false)
		}
		return m, nil
	case sessionActionedMsg:
		if msg.err != nil {
			m.showNotice("Could not "+msg.action+" session", msg.err.Error(), false)
			return m, nil
		}
		m.applySessionAction(msg)
		return m, nil
	case providersListedMsg:
		if msg.err != nil {
			m.showNotice("Could not list models", msg.err.Error(), false)
		} else {
			m.showModels(msg.providers, msg.switching)
		}
		return m, nil
	case sessionCreatedMsg:
		if msg.err != nil {
			m.showNotice("Could not create session", msg.err.Error(), false)
			return m, nil
		}
		m.session = msg.output.SessionMeta
		m.contextWindow = msg.output.ContextWindow
		m.thinking = msg.output.Thinking
		m.availableThinking = msg.output.AvailableThinking
		m.modalities = msg.output.Modalities
		m.pending = nil
		m.reflow()
		m.client.SetSession(msg.output.ID)
		m.blocks = nil
		m.checklist = contracts.TaskChecklistState{}
		m.used = 0
		m.modal = nil
		m.refreshTranscript(true)
		return m, m.startSessionObserver(msg.output.ID)
	case modelSetMsg:
		if msg.err != nil {
			m.appendError(msg.err)
		} else {
			m.session = msg.output.SessionMeta
			m.contextWindow = msg.output.ContextWindow
			m.thinking = msg.output.Thinking
			m.availableThinking = msg.output.AvailableThinking
			m.modalities = msg.output.Modalities
			m.pending = nil
			m.reflow()
			m.blocks = append(m.blocks, block{role: "system", text: "Model switched to " + msg.output.Model + " @ " + msg.output.Provider})
		}
		m.modal = nil
		m.refreshTranscript(true)
		return m, nil
	case compactedMsg:
		m.working = false
		if msg.err != nil {
			m.appendError(msg.err)
		} else {
			m.session = *msg.meta
			m.used = 0
			m.blocks = blocksFromRecords(msg.records)
			m.checklist = taskChecklistFromRecords(msg.records)
		}
		m.refreshTranscript(true)
		return m, nil
	case timelineLoadedMsg:
		if msg.err != nil {
			m.showNotice("Could not load timeline", msg.err.Error(), false)
		} else {
			m.showTimeline(msg.view)
		}
		return m, nil
	case branchSelectedMsg:
		if msg.err != nil {
			m.appendError(msg.err)
		} else {
			m.checklist = msg.checklist
			m.blocks = append(m.blocks, block{role: "system", text: branchSelectionLabel(msg.event)})
		}
		m.modal = nil
		m.refreshTranscript(true)
		return m, nil
	case approvalResolvedMsg:
		if msg.err != nil {
			m.appendError(msg.err)
			m.refreshTranscript(true)
		}
		return m, nil
	case renamedMsg:
		if msg.err != nil {
			m.appendError(msg.err)
		} else if msg.meta != nil {
			m.session.Name = msg.meta.Name
			m.blocks = append(m.blocks, block{role: "system", text: "Session renamed to " + msg.meta.Name})
		}
		m.refreshTranscript(true)
		return m, nil
	case animationTickMsg:
		if m.working {
			return m, animationTickCmd()
		}
		return m, nil
	case cancelArmTimeoutMsg:
		if m.cancelArmed && msg.id == m.cancelArmID {
			m.cancelArmed = false
			m.workStatus = m.cancelStatus
			m.cancelStatus = ""
			m.reflow()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if m.modal == nil {
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			m.updateCommandMenu()
			m.reflow()
			return m, cmd
		}
		return m, nil
	case tea.MouseMsg:
		if m.modal == nil {
			mouse := msg.Mouse()
			if len(m.areas) > 1 && (mouse.Y < m.areas[1].y || mouse.Y >= m.areas[1].y+m.areas[1].height) {
				return m, nil
			}
			if wheel, ok := msg.(tea.MouseWheelMsg); ok {
				switch wheel.Button {
				case tea.MouseWheelUp:
					m.scrollTranscript(-transcriptScrollStep)
				case tea.MouseWheelDown:
					m.scrollTranscript(transcriptScrollStep)
				}
			}
			return m, nil
		}
		return m, nil
	}

	if m.modal == nil {
		var composerCmd tea.Cmd
		m.composer, composerCmd = m.composer.Update(message)
		m.reflow()
		return m, composerCmd
	}
	return m, nil
}

func (m *chatModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	// A pending decision owns the keyboard: the turn is blocked until it is
	// answered, so the composer is not accepting prompts anyway.
	if model, cmd, handled := m.handleApprovalKey(key); handled {
		return model, cmd
	}
	if m.working && key == "esc" {
		if !m.cancelArmed {
			m.cancelArmed = true
			m.cancelArmID++
			m.cancelStatus = m.workStatus
			m.workStatus = "press esc again to cancel"
			m.reflow()
			return m, cancelArmTimeoutCmd(m.cancelArmID)
		} else {
			m.cancelArmed = false
			m.cancelStatus = ""
			m.workStatus = "canceling"
			m.reflow()
			return m, cancelTurnCmd(m.ctx, m.client, m.session.ID)
		}
	}
	if m.modal != nil {
		return m, m.handleModalKey(msg)
	}
	switch msg.Key().Keystroke() {
	case "ctrl+x":
		return m, editComposerCmd(m.composer.Value())
	case "ctrl+v":
		return m, m.pasteImage()
	case "ctrl+u":
		// Clear staged images and the composer text; with none staged, ctrl+u
		// keeps its normal delete-before-cursor behavior.
		if len(m.pending) > 0 {
			m.composer.Reset()
			m.clearAttachments()
			return m, nil
		}
	}
	if m.working {
		// A running turn does not stop the composer: a prompt typed now is
		// queued by the session and served next. The session workspace and the
		// model picker stay closed, because both reattach the client to a
		// different session rather than feed this turn.
		switch key {
		case "ctrl+o", "ctrl+m":
			return m, nil
		}
	}
	if key == "ctrl+o" {
		m.modal = loadingModal("Loading sessions")
		return m, listSessionsCmd(m.ctx, m.client)
	}
	if key == "ctrl+m" {
		m.modal = loadingModal("Loading models")
		return m, listProvidersCmd(m.ctx, m.client, true)
	}
	if key == "ctrl+t" {
		m.cycleThinking()
		return m, nil
	}
	if m.commandMenu {
		matches := matchingCommands(m.composer.Value())
		switch key {
		case "esc":
			m.commandMenu = false
			m.reflow()
			return m, nil
		case "up":
			m.commandSelection = (m.commandSelection - 1 + len(matches)) % len(matches)
			return m, nil
		case "down":
			m.commandSelection = (m.commandSelection + 1) % len(matches)
			return m, nil
		case "tab":
			m.composer.SetValue(matches[m.commandSelection].name)
			m.commandMenu = false
			m.reflow()
			return m, nil
		case "enter":
			for _, command := range matches {
				if command.name == m.composer.Value() {
					m.commandMenu = false
					return m, m.submit()
				}
			}
			m.composer.SetValue(matches[m.commandSelection].name)
			m.commandMenu = false
			m.reflow()
			return m, nil
		}
	}
	// Scrolling owns the transcript, so the layout does not have to run again:
	// scrollTranscript already placed the window.
	if key == "pgup" {
		m.scrollTranscript(-m.transcriptHeight())
		return m, nil
	}
	if key == "pgdown" {
		m.scrollTranscript(m.transcriptHeight())
		return m, nil
	}
	if key == "ctrl+up" {
		m.scrollTranscript(-transcriptScrollStep)
		return m, nil
	}
	if key == "ctrl+down" {
		m.scrollTranscript(transcriptScrollStep)
		return m, nil
	}
	if key == "enter" {
		if m.filePicker != nil {
			if len(m.filePicker.matches) > 0 {
				path := m.filePicker.matches[m.filePicker.selected]
				value := m.composer.Value()
				m.composer.SetValue(value[:m.filePicker.start] + "@" + path + value[m.filePicker.start+1+len(m.filePicker.query):])
				m.composer.CursorEnd()
			}
			m.filePicker = nil
			m.reflow()
			return m, nil
		}
		return m, m.submit()
	}

	if m.filePicker != nil {
		switch key {
		case "esc":
			m.filePicker = nil
			m.reflow()
			return m, nil
		case "up":
			m.filePicker.selected = (m.filePicker.selected - 1 + max(len(m.filePicker.matches), 1)) % max(len(m.filePicker.matches), 1)
			return m, nil
		case "down", "tab":
			m.filePicker.selected = (m.filePicker.selected + 1) % max(len(m.filePicker.matches), 1)
			return m, nil
		}
	}

	var cmd tea.Cmd
	before := m.composer.Value()
	m.composer, cmd = m.composer.Update(msg)
	value := m.composer.Value()
	if m.filePicker != nil {
		if len(value) < m.filePicker.start || !strings.HasPrefix(value[m.filePicker.start:], "@") || strings.ContainsAny(value[m.filePicker.start+1:], " \t\n") {
			m.filePicker = nil
		} else {
			m.filePicker.filter(value[m.filePicker.start+1:])
		}
	} else if value == before+"@" && m.session.Cwd != "" {
		m.filePicker = &filePicker{files: scanWorkspaceFiles(m.session.Cwd), start: len(before)}
		m.filePicker.filter("")
	}
	m.updateCommandMenu()
	m.reflow()
	return m, cmd
}

func (m *chatModel) cycleThinking() {
	if len(m.availableThinking) == 0 {
		m.showNotice("Thinking unavailable", "The current model does not support configurable thinking.", false)
		return
	}
	next := m.availableThinking[0]
	for i, pattern := range m.availableThinking {
		if pattern != m.thinking {
			continue
		}
		if i+1 < len(m.availableThinking) {
			next = m.availableThinking[i+1]
		}
		break
	}
	m.thinking = next
	m.reflow()
}

// handleSessionListKey routes the keys the session list owns: "d" deletes a
// saved session and "c" closes one the engine is holding. A delete is armed by
// the first "d" and confirmed by the second, and every other key drops the arm,
// so the confirmation cannot outlive the row it was meant for.
func (m *chatModel) handleSessionListKey(key string) (tea.Cmd, bool) {
	if len(m.modal.options) == 0 {
		return nil, false
	}
	selected := clamp(m.modal.selected, 0, len(m.modal.options)-1)
	option := m.modal.options[selected]
	// Every key drops an arm first; "d" puts it back. Nothing else can carry a
	// confirmation over to a later press.
	armed := m.modal.pendingDelete
	m.modal.pendingDelete = uuid.Nil

	switch key {
	case "d":
		if option.session == uuid.Nil {
			return nil, false
		}
		if armed == option.session {
			return deleteSessionCmd(m.ctx, m.client, option.session), true
		}
		m.modal.pendingDelete = option.session
		return nil, true
	case "c":
		if option.session == uuid.Nil {
			return nil, false
		}
		if !option.sessionActive {
			// The engine has nothing to tear down: the session is saved history,
			// already closed. Say so here rather than spend a round trip on it.
			m.showNotice("Session is not open", "Only a session the engine is holding can be closed.", false)
			return nil, true
		}
		return closeSessionCmd(m.ctx, m.client, option.session), true
	case "esc":
		if armed != uuid.Nil {
			return nil, true
		}
	}
	return nil, false
}

func (m *chatModel) handleModalKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.modal.kind == modalSessions {
		if cmd, handled := m.handleSessionListKey(key); handled {
			return cmd
		}
	}
	switch key {
	case "up", "left", "k", "h":
		m.modal.move(-1)
	case "down", "right", "j", "l", "tab":
		m.modal.move(1)
	case "shift+tab":
		m.modal.move(-1)
	case "esc":
		if !m.modal.required {
			m.modal = nil
		}
	case "enter":
		return m.activateModal(m.modal.selected)
	default:
		if msg.Key().Text != "" {
			runes := []rune(msg.Key().Text)
			if len(runes) == 1 {
				if index, ok := m.modal.optionForShortcut(runes[0]); ok {
					return m.activateModal(index)
				}
			}
		}
	}
	return nil
}

func (m *chatModel) activateModal(index int) tea.Cmd {
	if m.modal == nil || index < 0 || index >= len(m.modal.options) {
		return nil
	}
	modal, option := m.modal, m.modal.options[index]
	switch modal.kind {
	case modalSessions:
		if option.session == uuid.Nil {
			m.modal = loadingModal("Loading models")
			return listProvidersCmd(m.ctx, m.client, false)
		}
		m.modal = loadingModal("Opening session")
		return loadSessionCmd(m.ctx, m.client, option.session)
	case modalModels:
		m.modal = loadingModal("Applying model")
		if modal.switching {
			return setModelCmd(m.ctx, m.client, option.provider, option.model)
		}
		return createSessionCmd(m.ctx, m.client, option.provider, option.model)
	case modalCommands:
		m.modal = nil
	case modalTimeline:
		if option.event != nil {
			m.modal = loadingModal("Selecting branch")
			return selectBranchCmd(m.ctx, m.client, m.session.ID, *option.event)
		}
	case modalNotice:
		m.modal = nil
	}
	return nil
}

func (m *chatModel) filePickerFiles() []string {
	if m.session.Cwd == "" {
		return nil
	}
	return scanWorkspaceFiles(m.session.Cwd)
}

func (m *chatModel) submit() tea.Cmd {
	prompt := strings.TrimSpace(m.composer.Value())
	if prompt == "" {
		return nil
	}
	images := append([]contracts.ImageInput(nil), m.pending...)
	if strings.HasPrefix(prompt, "/") && len(images) == 0 {
		m.composer.Reset()
		m.commandMenu = false
		m.reflow()
		return m.runCommand(prompt)
	}
	if m.session.ID == uuid.Nil {
		// Keep the draft and attachments so the user can retry once a session
		// exists.
		m.appendError(errors.New("no active session"))
		m.refreshTranscript(true)
		return nil
	}
	if len(images) > 0 && !m.modelSupportsImage() {
		m.appendError(fmt.Errorf("model %q does not support image input", m.session.Model))
		m.refreshTranscript(true)
		return nil
	}
	m.composer.Reset()
	m.commandMenu = false
	m.reflow()

	input := contracts.UserInput{Text: encodeFileReferences(prompt, m.filePickerFiles()), Images: images}
	// A prompt sent while a turn is running is queued by the session and served
	// next. The turn already in flight owns the status, so it is left as the
	// events reported it.
	ownsWork := !m.working
	if ownsWork {
		// The status the session will confirm; the events overwrite it.
		m.status = contracts.SessionBusy
		m.working = true
		m.workBegan = time.Now()
	}
	m.cancelArmed = false
	m.refreshTranscript(true)
	thinking := m.thinking
	// Attachments are consumed only once dispatch has successfully begun.
	m.pending = nil
	return tea.Batch(func() tea.Msg {
		return messageSentMsg{ownsWork: ownsWork, err: m.client.SendMessage(m.ctx, input, thinking)}
	}, animationTickCmd())
}

func cancelTurnCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		return turnCanceledMsg{err: client.CancelTurn(ctx, id)}
	}
}

func (m *chatModel) startSessionObserver(sessionID uuid.UUID) tea.Cmd {
	if m.sessionCancel != nil {
		m.sessionCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.sessionCancel = cancel
	m.sessionObserverID++
	observerID := m.sessionObserverID
	m.sessionStream = make(chan tea.Msg, 256)
	stream := m.sessionStream
	emit := func(message tea.Msg) {
		select {
		case stream <- message:
		case <-ctx.Done():
		}
	}
	join := func() tea.Msg {
		defer cancel()
		err := m.client.JoinSession(ctx, sessionID, func(view glue.SessionView) {
			emit(sessionViewMsg{sessionID: sessionID, observerID: observerID, view: view})
		}, func(event contracts.EventStream) {
			emit(sessionEventMsg{sessionID: sessionID, observerID: observerID, event: event})
		})
		return sessionJoinDoneMsg{sessionID: sessionID, observerID: observerID, err: err}
	}
	return tea.Batch(waitStream(ctx, stream), join)
}

func (m *chatModel) applySessionView(view glue.SessionView) {
	m.session = view.Meta
	m.blocks = blocksFromMessages(view.History)
	m.checklist = view.Checklist
	// The in-flight log is replayed through the same handler the live stream
	// uses, so a client that joins mid-generation renders the generation the
	// same way it would have rendered it live.
	for _, event := range view.InFlight {
		m.applySessionEvent(event)
	}
	m.applySessionStatus(view.Status)
	if view.PendingApproval != nil {
		m.showApproval(approvalFromRequest(view.PendingApproval))
	} else {
		m.clearApproval()
	}
}

func (m *chatModel) applySessionStatus(status contracts.SessionStatus) {
	m.status = status
	switch status {
	case contracts.SessionBusy, contracts.SessionWaitingApproval, contracts.SessionCompacting:
		m.working = true
	case contracts.SessionIdle:
		m.working = false
		m.stopStreaming()
		m.workStatus = ""
		m.cancelArmed = false
	case contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone:
		m.working = false
		m.stopStreaming()
		m.workStatus = ""
		m.clearApproval()
	}
	if m.working && m.workBegan.IsZero() {
		m.workBegan = time.Now()
	}
}

func (m *chatModel) applySessionEvent(event contracts.EventStream) {
	content := ""
	if event.Content != nil {
		content = *event.Content
	}
	switch event.Kind {
	case contracts.EventSessionTransitionState:
		m.applySessionStatus(contracts.SessionStatus(content))
	case contracts.EventSessionFatal:
		m.applySessionStatus(contracts.SessionTombstone)
		m.appendError(errors.New(content))
	case contracts.EventSubscriberGap:
		m.appendError(errors.New(content))
	case contracts.EventMessageFromAgentInbox:
		images := 0
		if event.Attachments != nil {
			images = event.Attachments.ImageCount
		}
		m.blocks = append(m.blocks, block{role: "user", text: content, imageCount: images})
	case contracts.EventToolCall:
		if event.ToolCall != nil {
			name := string(event.ToolCall.Function.Name)
			m.toolCallNames[event.ToolCall.ID] = name
			if isSkillTool(name) {
				m.skillNames[event.ToolCall.ID] = skillNameFromCall(*event.ToolCall)
				break
			}
			m.appendToolEvent("call", contracts.ToolCallDisplay(*event.ToolCall))
		} else {
			m.appendToolEvent("call", content)
		}
	case contracts.EventToolResult:
		if event.ToolResult == nil {
			m.appendToolEvent("result", content)
			break
		}
		if isSkillTool(string(event.ToolResult.CallName)) {
			break
		}
		result := string(event.ToolResult.CallName) + " [" + string(event.ToolResult.Status) + "]"
		if event.ToolResult.Error != "" {
			result += ": " + event.ToolResult.Error
		} else if event.ToolResult.Output != "" {
			result += "\n" + event.ToolResult.Output
		}
		m.appendToolEvent("result", result)
	case contracts.EventToolTaskCheckList:
		if event.ToolResult != nil {
			if state, err := decodeTaskChecklist(event.ToolResult.Output); err == nil {
				m.checklist = state
			}
		} else if state, err := decodeTaskChecklist(content); err == nil {
			m.checklist = state
		}
	case contracts.EventSkillLoad:
		name := string(contracts.ReadSkillTool)
		if event.ToolResult != nil {
			if loaded := m.skillNames[event.ToolResult.CallID]; loaded != "" {
				name = loaded
			}
		}
		m.blocks = append(m.blocks, block{role: "skill", text: "Skill loaded: " + name})
	case contracts.EventApprovalRequested:
		if event.HITLCall != nil {
			m.showApproval(approvalFromRequest(event.HITLCall))
		}
	case contracts.EventApprovalResolved:
		m.handleApprovalUpdate(ApprovalUpdate{Type: string(event.Kind), Approval: approvalFromRequest(event.HITLCall)})
	case contracts.EventCompactionExecuted:
		if event.Compaction == nil {
			break
		}
		switch {
		case event.Compaction.Err != "" && event.Compaction.Automatic:
			// A manual failure is reported by the /compact result it answers; an
			// automatic one has no caller, so this event is the only report.
			m.stopStreaming()
			m.appendError(errors.New("compaction failed: " + event.Compaction.Err))
		case event.Compaction.Summary != "":
			m.appendDelta(event.Kind, event.Compaction.Summary)
		}
	case contracts.NotifyAgentUsage:
		var usage contracts.TokenUsage
		if json.Unmarshal([]byte(content), &usage) == nil {
			m.used = usage.TotalTokens
		}
	default:
		m.appendDelta(event.Kind, content)
	}
}

func blocksFromMessages(history []contracts.ChatMessage) []block {
	records := make([]store.Record, 0, len(history))
	for i := range history {
		message := history[i]
		records = append(records, store.Record{Kind: store.KindMessage, Message: &message})
	}
	return blocksFromRecords(records)
}

func waitStream(ctx context.Context, stream <-chan tea.Msg) tea.Cmd {
	if stream == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case message := <-stream:
			return message
		case <-ctx.Done():
			return nil
		}
	}
}

// ---- image attachments ----

// modelSupportsImage reports whether the selected model declares image input.
// An empty modality list means the model declares no multimodal input.
func (m *chatModel) modelSupportsImage() bool {
	for _, modality := range m.modalities {
		if modality == contracts.ModalityImage {
			return true
		}
	}
	return false
}

// pasteImage kicks off an asynchronous clipboard read. It performs the cheap
// preflight checks synchronously so failures are reported without spawning a
// process.
func (m *chatModel) pasteImage() tea.Cmd {
	if m.modal != nil {
		return nil
	}
	if m.session.ID == uuid.Nil {
		m.appendError(errors.New("no active session"))
		m.refreshTranscript(true)
		return nil
	}
	if !m.modelSupportsImage() {
		m.appendError(fmt.Errorf("model %q does not support image input", m.session.Model))
		m.refreshTranscript(true)
		return nil
	}
	if len(m.pending) >= vision.MaxImages {
		m.appendError(fmt.Errorf("at most %d images can be attached", vision.MaxImages))
		m.refreshTranscript(true)
		return nil
	}
	if m.clipboard == nil {
		m.appendError(ErrClipboardUnavailable)
		m.refreshTranscript(true)
		return nil
	}
	return pasteImageCmd(m.ctx, m.clipboard)
}

func pasteImageCmd(ctx context.Context, reader ClipboardReader) tea.Cmd {
	return func() tea.Msg {
		data, err := reader.ReadImage(ctx)
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		// Decoding is CPU-heavy for large images, so it runs here, off the
		// Bubble Tea update loop.
		image, err := vision.ValidateImage(data)
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		return clipboardImageMsg{image: image}
	}
}

func (m *chatModel) handleClipboardImage(msg clipboardImageMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.appendError(clipboardError(msg.err))
		m.refreshTranscript(true)
		return m, nil
	}
	if m.session.ID == uuid.Nil {
		m.appendError(errors.New("no active session"))
		m.refreshTranscript(true)
		return m, nil
	}
	if !m.modelSupportsImage() {
		m.appendError(fmt.Errorf("model %q does not support image input", m.session.Model))
		m.refreshTranscript(true)
		return m, nil
	}
	if len(m.pending) >= vision.MaxImages {
		m.appendError(fmt.Errorf("at most %d images can be attached", vision.MaxImages))
		m.refreshTranscript(true)
		return m, nil
	}
	image := msg.image
	candidate := append(append([]contracts.ImageInput(nil), m.pending...), image)
	if err := vision.ValidateBatch(candidate); err != nil {
		m.appendError(err)
		m.refreshTranscript(true)
		return m, nil
	}
	image.Name = clipboardImageName(len(m.pending)+1, image.MediaType)
	m.pending = append(m.pending, image)
	m.reflow()
	return m, nil
}

func clipboardError(err error) error {
	switch {
	case errors.Is(err, ErrClipboardUnavailable):
		return errors.New("clipboard image paste is unavailable: install wl-paste, xclip, or pngpaste")
	case errors.Is(err, ErrClipboardEmpty):
		return errors.New("no image found on the clipboard")
	case errors.Is(err, ErrClipboardTooLarge):
		return fmt.Errorf("clipboard image exceeds the %d MB limit", vision.MaxImageBytes>>20)
	default:
		return err
	}
}

// renderImageBadges renders the green [Image N] chips for a set of attachments.
func (m *chatModel) renderImageBadges(count int) string {
	if count <= 0 {
		return ""
	}
	badges := make([]string, 0, count)
	for i := range count {
		badges = append(badges, m.styles.imageBadge.Render(fmt.Sprintf("[Image %d]", i+1)))
	}
	return strings.Join(badges, " ")
}

// clearAttachments drops every staged image. Ctrl+U is the explicit action
// because the composer carries no marker text to delete.
func (m *chatModel) clearAttachments() {
	if len(m.pending) == 0 {
		return
	}
	m.pending = nil
	m.reflow()
}

func (m *chatModel) runCommand(command string) tea.Cmd {
	if strings.HasPrefix(command, "/rename") {
		name := strings.TrimSpace(strings.TrimPrefix(command, "/rename"))
		if m.session.ID == uuid.Nil {
			m.appendError(errors.New("no active session"))
			m.refreshTranscript(true)
			return nil
		}
		if name == "" {
			m.appendError(errors.New("usage: /rename <name>"))
			m.refreshTranscript(true)
			return nil
		}
		return renameSessionCmd(m.ctx, m.client, m.session.ID, name)
	}
	switch command {
	case "/model":
		m.modal = loadingModal("Loading models")
		return listProvidersCmd(m.ctx, m.client, true)
	case "/sessions":
		m.modal = loadingModal("Loading sessions")
		return listSessionsCmd(m.ctx, m.client)
	case "/new":
		m.modal = loadingModal("Loading models")
		return listProvidersCmd(m.ctx, m.client, false)
	case "/compact":
		if m.session.ID == uuid.Nil {
			m.appendError(errors.New("no active session"))
			m.refreshTranscript(true)
			return nil
		}
		m.working, m.workBegan = true, time.Now()
		return tea.Batch(compactCmd(m.ctx, m.client, m.session.ID), animationTickCmd())
	case "/timeline":
		m.modal = loadingModal("Loading timeline")
		return loadTimelineCmd(m.ctx, m.client, m.session.ID)
	case "/help":
		m.blocks = append(m.blocks, block{role: "system", text: strings.Join([]string{
			"Enter sends · Shift+Enter adds a line · PageUp/PageDown scroll · Ctrl+O sessions · Ctrl+N new · Ctrl+T thinking",
			"Ctrl+V paste image · Ctrl+U clear images",
			"/new · /sessions · /model · /compact · /timeline · /rename · /quit",
		}, "\n")})
		m.refreshTranscript(true)
	case "/quit", "/exit":
		return tea.Quit
	default:
		m.appendError(fmt.Errorf("unknown command %q", command))
		m.refreshTranscript(true)
	}
	return nil
}

func (m *chatModel) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		return tea.NewView("")
	}
	header := m.headerView()
	status := m.statusView()
	sessionRow := m.sessionRowView()
	checklist := m.checklistView()
	hitl := m.hitlView()
	commands := m.commandMenuView()
	files := renderFilePicker(m.filePicker, m.width, m.styles)
	attachments := m.attachmentsView()
	composer := m.composerView()
	parts := []string{header, m.viewport.View(), hitl, checklist, sessionRow, commands, files, attachments, composer, status}
	if len(m.areas) == len(parts) {
		parts[5] = m.commandMenuViewForHeight(m.areas[5].height)
		for i := range parts {
			parts[i] = fitArea(m.areas[i], parts[i])
		}
	}
	visibleParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			visibleParts = append(visibleParts, part)
		}
	}
	base := lipgloss.JoinVertical(lipgloss.Left, visibleParts...)
	if m.modal != nil && m.modal.kind != modalTimeline {
		base = renderSelectionScreen(m.modal, m.width, m.height, m.styles)
	} else if m.modal != nil {
		dialog := renderModal(m.modal, m.width, m.height, m.styles)
		base = lipgloss.NewCompositor(
			lipgloss.NewLayer(base),
			centeredLayer(dialog, m.width, m.height).Z(1),
		).Render()
	}
	v := tea.NewView(base)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "infai harness"
	v.BackgroundColor = themeBackground()
	v.ForegroundColor = themeForeground()
	return v
}

// reflow lays out the chrome and gives the transcript the space that is left.
// It re-syncs the window only when the transcript's share of the screen changed:
// a same-shape reflow leaves the viewport exactly as it was.
func (m *chatModel) reflow() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	previousViewportWidth := m.viewport.Width()
	previousViewportHeight := m.viewport.Height()
	wasAtBottom := m.atBottom()
	m.refreshInputMark()
	m.composer.SetWidth(contentWidth(m.styles.composer, m.width))
	header := m.headerView()
	status := m.statusView()
	sessionRow := m.sessionRowView()
	checklist := m.checklistView()
	hitl := m.hitlView()
	commands := m.commandMenuView()
	files := renderFilePicker(m.filePicker, m.width, m.styles)
	attachments := m.attachmentsView()
	composer := m.composerView()
	m.areas = layoutRows(m.width, m.height, intrinsic(header), fill(), intrinsic(hitl), intrinsic(checklist), intrinsic(sessionRow), intrinsic(commands), intrinsic(files), intrinsic(attachments), intrinsic(composer), intrinsic(status))
	main := m.areas[1]
	m.viewport.SetWidth(main.width)
	m.viewport.SetHeight(main.height)
	if previousViewportWidth != main.width || previousViewportHeight != main.height {
		m.refreshTranscript(wasAtBottom)
	}
}

func (m *chatModel) headerView() string {
	content := m.styles.brand.Render("INFAI") + " " + m.styles.headerMeta.Render("HARNESS")
	return fullWidth(m.styles.header, m.width, content)
}

func (m *chatModel) statusView() string {
	separator := m.styles.status.Render("  ·  ")
	var rest string
	if m.session.ID == uuid.Nil {
		rest = m.styles.status.Render("choose a session to begin")
	} else {
		pct := 0
		if m.contextWindow > 0 {
			pct = min(int(m.used*100/m.contextWindow), 100)
		}
		thinking := m.thinking
		if thinking == "" {
			thinking = "off"
		}
		fields := []string{
			m.styles.status.Render(fmt.Sprintf("%s (%s)", m.session.Model, m.session.Provider)),
		}
		if m.session.Cwd != "" {
			fields = append(fields, m.styles.status.Render(m.session.Cwd))
		}
		fields = append(fields,
			m.styles.active.Render("thinking "+string(thinking)),
			m.styles.status.Render("ctx ")+contextProgressBar(m.styles, pct, 6)+m.styles.status.Render(fmt.Sprintf(" %d%% %s/%s", pct, tokenCount(m.used), tokenCount(m.contextWindow))),
		)
		rest = strings.Join(fields, separator)
	}
	return fullWidth(lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1), m.width, rest)
}

// attachmentsView shows the staged attachments as [Image N] chips above the
// composer. The composer text itself stays clean.
func (m *chatModel) attachmentsView() string {
	if len(m.pending) == 0 {
		return ""
	}
	return fullWidth(lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1), m.width, m.renderImageBadges(len(m.pending)))
}

// checklistView is the task checklist, rendered above the status row. It is
// turn state rather than chrome, so it sits with the transcript instead of
// stacking on top of the input.
func (m *chatModel) checklistView() string {
	checklist := m.taskChecklistView(max(m.width-2, 1))
	if checklist == "" {
		return ""
	}
	return fullWidth(lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1), m.width, checklist)
}

// sessionRowView is the row above the composer: which session this is, what
// kind of agent runs it, and what the session is doing. The spinner and the
// elapsed time belong to the turn in flight, so they sit beside the status only
// while one is running.
// sessionRowView is the row above the composer: the marks that describe the
// session, then the session name in the room left over. Every mark is a glyph
// and the word it stands for, the way the session list names them, and the whole
// run sits flush right, where the status row always was. The spinner and the
// elapsed time belong to the turn in flight, so they appear only while one runs.
func (m *chatModel) sessionRowView() string {
	if m.session.ID == uuid.Nil {
		return ""
	}
	separator := m.styles.status.Render("  ·  ")
	status := describeSessionStatus(m.status, m.styles)

	kindGlyph, kindStyle := agentKindMark(m.session.AgentKind, m.styles)
	kindWord := agentKindLabel(m.session.AgentKind)

	kindGroup := func(withWord bool) string {
		if kindGlyph == "" {
			return ""
		}
		if withWord && kindWord != "" {
			return kindStyle.Render(kindGlyph + " " + kindWord)
		}
		return kindStyle.Render(kindGlyph)
	}
	statusGroup := func(withWord bool) string {
		if withWord {
			return m.sessionStatusView()
		}
		return status.style.Render(status.glyph)
	}
	groups := func(withWords bool) string {
		fields := make([]string, 0, 4)
		for _, group := range []string{kindGroup(withWords), statusGroup(withWords)} {
			if group != "" {
				fields = append(fields, group)
			}
		}
		if m.working {
			fields = append(fields, m.styles.statusBusy.Render(fmt.Sprintf("%s %s", spinnerFrame(m.workBegan), time.Since(m.workBegan).Round(time.Second))))
		}
		// Transient activity: a provider retry notice, or the cancel prompt.
		if detail := m.workStatus; detail != "" {
			fields = append(fields, m.styles.statusBusy.Render(detail))
		}
		return strings.Join(fields, separator)
	}

	inner := max(m.width-2, 1)
	// The words are the first thing to go, exactly as in the session list: the
	// glyphs still carry the kind and the status.
	marks := groups(false)
	if inner-lipgloss.Width(groups(true))-sessionRowGap >= minSessionNameRoom {
		marks = groups(true)
	}
	name := ""
	if room := inner - lipgloss.Width(marks) - sessionRowGap; room >= minSessionNameRoom {
		name = m.session.Name
		if name == "" {
			name = "Untitled session"
		}
		name = m.styles.sessionName.Render(truncateLine(name, room))
	}
	gap := strings.Repeat(" ", max(inner-lipgloss.Width(name)-lipgloss.Width(marks), 0))
	// The blank row above is what separates the session row from the transcript;
	// the bottom line sits flush against the composer.
	return m.styles.statusRow.PaddingTop(1).Render(" " + name + gap + marks + " ")
}

// sessionRowGap is the space kept between the session name and the marks, and
// minSessionNameRoom is the room below which the name says too little to take
// space from them.
const (
	sessionRowGap      = 4
	minSessionNameRoom = 12
)

// sessionStatusView renders the status the session reported for itself. Every
// status has its own label and colour, so a session waiting on an approval or
// one that has concluded never reads the same as one that is simply idle.
func (m *chatModel) sessionStatusView() string {
	descriptor := describeSessionStatus(m.status, m.styles)
	return descriptor.style.Render(descriptor.glyph + " " + descriptor.label)
}

// tokenCount renders a token total compactly: 950, 41.2k, 128k, 1.2M. The
// status line has one row to say how much context is in use, and a raw 128000
// does not fit beside the bar.
func tokenCount(tokens uint64) string {
	for _, unit := range []struct {
		divisor uint64
		suffix  string
	}{{1_000_000, "M"}, {1_000, "k"}} {
		if tokens < unit.divisor {
			continue
		}
		whole := tokens / unit.divisor
		remainder := (tokens % unit.divisor) * 10 / unit.divisor
		if whole < 100 && remainder > 0 {
			return fmt.Sprintf("%d.%d%s", whole, remainder, unit.suffix)
		}
		return fmt.Sprintf("%d%s", whole, unit.suffix)
	}
	return fmt.Sprintf("%d", tokens)
}

// contextBarLevels splits a cell into eighths, so a short bar still moves in
// small steps: six cells carry forty-eight positions instead of six.
var contextBarLevels = [...]string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

// contextProgressBar draws the context bar as a run of filled cells, a partial
// boundary cell, and dim cells for what is left.
func contextProgressBar(styles harnessStyles, percent, width int) string {
	eighths := min(max(percent, 0), 100) * width * 8 / 100
	full, partial := eighths/8, eighths%8
	if percent > 0 && full == 0 && partial == 0 {
		partial = 1
	}
	if full >= width {
		full, partial = width, 0
	}
	filled := strings.Repeat("█", full) + contextBarLevels[partial]
	return styles.active.Render(filled) +
		lipgloss.NewStyle().Foreground(everforest.SurfaceAlt).Render(strings.Repeat("░", max(width-lipgloss.Width(filled), 0)))
}

func (m *chatModel) taskChecklistView(width int) string {
	if len(m.checklist.Items) == 0 {
		return ""
	}
	completed := 0
	for _, item := range m.checklist.Items {
		if item.Status == contracts.TaskCompleted {
			completed++
		}
	}
	lines := []string{m.styles.system.Bold(true).Render(fmt.Sprintf("Task Checklist %d/%d complete", completed, len(m.checklist.Items)))}
	visible := min(len(m.checklist.Items), 4)
	for _, item := range m.checklist.Items[:visible] {
		marker := "○"
		style := m.styles.inactive
		switch item.Status {
		case contracts.TaskInProgress:
			marker, style = "◐", m.styles.statusBusy
		case contracts.TaskCompleted:
			marker, style = "✓", m.styles.active
		}
		line := marker + " " + item.Title
		if item.Description != "" {
			line += "  ·  " + item.Description
		}
		lines = append(lines, style.Render(truncateLine(line, width)))
	}
	if len(m.checklist.Items) > visible {
		lines = append(lines, m.styles.muted.Render(fmt.Sprintf("… %d more", len(m.checklist.Items)-visible)))
	}
	return strings.Join(lines, "\n")
}

func (m *chatModel) composerView() string {
	if m.composerWaiting {
		return fullWidth(m.styles.composerWaiting, m.width, m.composer.View())
	}
	return fullWidth(m.styles.composer, m.width, m.composer.View())
}

func (m *chatModel) commandMenuView() string {
	return m.commandMenuViewForHeight(len(harnessCommands))
}

func (m *chatModel) commandMenuViewForHeight(height int) string {
	if !m.commandMenu {
		return ""
	}
	return renderCommandMenu(matchingCommands(m.composer.Value()), m.commandSelection, m.width, height, m.styles)
}

func (m *chatModel) updateCommandMenu() {
	matches := matchingCommands(m.composer.Value())
	m.commandMenu = len(matches) > 0
	if m.commandSelection >= len(matches) {
		m.commandSelection = max(len(matches)-1, 0)
	}
}

func fullWidth(style lipgloss.Style, width int, content string) string {
	return style.Width(width).Render(content)
}

// refreshTranscript brings the viewport back in step with the transcript.
// follow pins the view to the newest output; otherwise the scroll position
// stays where the reader left it.
func (m *chatModel) refreshTranscript(follow bool) {
	if m.viewport.Width() <= 0 {
		return
	}
	if follow {
		m.followTranscript()
		return
	}
	m.syncTranscript()
}

func (m *chatModel) renderBlock(entry *block, width int, streaming bool) string {
	var content string
	switch entry.role {
	case "user":
		content = renderChatMarker("●", m.styles.userMarker, m.styles.assistant, renderFileReferences(entry.text), width)
		if badges := m.renderImageBadges(entry.imageCount); badges != "" {
			content += "\n" + strings.Repeat(" ", lipgloss.Width("●")+1) + badges
		}
	case "assistant":
		text := entry.text
		if !streaming {
			text = m.renderMarkdown(text, max(width-2, 1))
		}
		content = renderChatMarker("●", m.styles.active, lipgloss.NewStyle(), text, width)
	case "thinking":
		text := entry.text
		if !streaming {
			text = m.renderThinkingMarkdown(text, max(width-2, 1))
		}
		content = renderChatMarker("◌", m.styles.muted, lipgloss.NewStyle(), text, width)
	case "error":
		content = m.styles.error.Width(width).Render("ERROR  " + entry.text)
	case "system", "status":
		content = m.styles.system.Width(width).Render("· " + entry.text)
	case "compaction":
		content = m.styles.thinking.Width(width).Render("CONTEXT COMPACTED")
		if body := strings.Trim(m.renderThinkingMarkdown(entry.text, width), "\n"); body != "" {
			content += "\n" + body
		}
	case "skill":
		content = renderChatMarker("✦", m.styles.skill, m.styles.skill, entry.text, width)
	case "tool":
		marker := "▲"
		markerStyle := m.styles.system
		if entry.toolKind == "result" {
			marker = "▼"
			markerStyle = m.styles.active
			if entry.toolStatus != "success" {
				markerStyle = m.styles.error
			}
		}
		switch {
		case entry.toolKind == "call" && entry.toolName == string(contracts.EditTool) && entry.toolArgs != "":
			content = renderEditDiffBlock(marker, markerStyle, m.styles, entry.toolArgs, width)
		case entry.toolKind == "call" && entry.toolName == string(contracts.WriteTool) && entry.toolArgs != "":
			content = renderWriteDiffBlock(marker, markerStyle, m.styles, entry.toolArgs, width)
		default:
			content = renderToolMarker(marker, markerStyle, m.styles.tool, entry.toolName, entry.text, width)
		}
	}
	return content
}

func renderChatMarker(marker string, markerStyle, bodyStyle lipgloss.Style, text string, width int) string {
	indent := lipgloss.Width(marker) + 1
	bodyWidth := max(width-indent, 1)
	lines := strings.Split(strings.Trim(lipgloss.Wrap(text, bodyWidth, ""), "\n"), "\n")
	for i := range lines {
		lines[i] = bodyStyle.Render(lines[i])
		if i == 0 {
			lines[i] = markerStyle.Render(marker) + " " + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", indent) + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func renderToolMarker(marker string, markerStyle, bodyStyle lipgloss.Style, name, text string, width int) string {
	detail := strings.TrimSpace(text)
	if name != "" && (detail == name || strings.HasPrefix(detail, name+" ") || strings.HasPrefix(detail, name+"\n")) {
		detail = strings.TrimSpace(strings.TrimPrefix(detail, name))
	}
	diff := name == string(contracts.EditTool)
	body := strings.TrimSpace(name + " " + detail)
	indent := lipgloss.Width(marker) + 1
	lines := strings.Split(strings.Trim(lipgloss.Wrap(body, max(width-indent, 1), ""), "\n"), "\n")
	for i := range lines {
		if i == 0 {
			rest := strings.TrimPrefix(lines[i], name)
			lineStyle := bodyStyle
			if diff {
				lineStyle = diffLineStyle(bodyStyle, rest)
			}
			lines[i] = markerStyle.Render(marker) + " " + markerStyle.Bold(true).Render(name) + lineStyle.Render(rest)
			continue
		}
		lineStyle := bodyStyle
		if diff {
			lineStyle = diffLineStyle(bodyStyle, lines[i])
		}
		lines[i] = strings.Repeat(" ", indent) + lineStyle.Render(lines[i])
	}
	return strings.Join(lines, "\n")
}

func diffLineStyle(base lipgloss.Style, line string) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff --git"):
		return base.Foreground(everforest.Aqua)
	case strings.HasPrefix(line, "@@"):
		return base.Foreground(everforest.Blue)
	case strings.HasPrefix(line, "+"):
		return base.Foreground(everforest.Green)
	case strings.HasPrefix(line, "-"):
		return base.Foreground(everforest.Red)
	}
	return base
}

type diffSegment struct {
	text string
	emph bool
	fg   color.Color
}

type diffRow struct {
	oldNum, newNum int
	marker         byte // ' ' context, '-' delete, '+' insert, '@' hunk header
	text           string
	segments       []diffSegment
}

// renderEditDiffBlock renders an edit tool call as a GitHub-style unified diff:
// two line-number gutters, a marker column, full-row red/green backgrounds, and
// word-level emphasis on the changed segments of paired lines.
func renderEditDiffBlock(marker string, markerStyle lipgloss.Style, styles harnessStyles, args string, width int) string {
	path, oldText, newText, replaceAll, ok := decodeEditArgs(args)
	if !ok {
		return renderToolMarker(marker, markerStyle, styles.tool, string(contracts.EditTool), prettyToolArguments(args), width)
	}
	rows := editDiffRows(path, oldText, newText)

	header := markerStyle.Render(marker) + " " + markerStyle.Bold(true).Render(string(contracts.EditTool)) + "  " + styles.tool.Render(path)
	if replaceAll {
		header += styles.muted.Render("  (every match)")
	}
	return renderDiffBlock(header, marker, rows, width, styles)
}

// renderWriteDiffBlock renders a write tool call as all-addition rows: new file
// contents shown with line numbers on the green insertion background.
func renderWriteDiffBlock(marker string, markerStyle lipgloss.Style, styles harnessStyles, args string, width int) string {
	path, content, ok := decodeWriteArgs(args)
	if !ok {
		return renderToolMarker(marker, markerStyle, styles.tool, string(contracts.WriteTool), prettyToolArguments(args), width)
	}
	lineCount, byteCount := 0, 0
	if content != "" {
		lineCount = strings.Count(content, "\n") + 1
	}
	byteCount = len([]byte(content))

	header := markerStyle.Render(marker) + " " + markerStyle.Bold(true).Render(string(contracts.WriteTool)) + "  " + styles.tool.Render(path) +
		styles.muted.Render(fmt.Sprintf("  (%d lines, %d bytes)", lineCount, byteCount))
	return renderDiffBlock(header, marker, writeDiffRows(path, content), width, styles)
}

func renderDiffBlock(header, marker string, rows []diffRow, width int, styles harnessStyles) string {
	indent := lipgloss.Width(marker) + 1
	oldWidth, newWidth := diffGutterWidths(rows)
	gutterWidth := oldWidth + newWidth + 4 // "old new marker " + trailing space
	codeWidth := max(width-indent-gutterWidth, 1)

	lines := []string{header}
	for _, row := range rows {
		for _, visual := range renderDiffRow(row, oldWidth, newWidth, codeWidth, styles) {
			lines = append(lines, strings.Repeat(" ", indent)+visual)
		}
	}
	return strings.Join(lines, "\n")
}

func decodeEditArgs(args string) (path, oldText, newText string, replaceAll, ok bool) {
	var input struct {
		Path       string  `json:"path"`
		OldString  *string `json:"old_string"`
		NewString  *string `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", "", "", false, false
	}
	if input.OldString != nil {
		oldText = *input.OldString
	}
	if input.NewString != nil {
		newText = *input.NewString
	}
	return input.Path, oldText, newText, input.ReplaceAll, true
}

func decodeWriteArgs(args string) (path, content string, ok bool) {
	var input struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return "", "", false
	}
	if input.Content != nil {
		content = *input.Content
	}
	return input.Path, content, true
}

func editDiffRows(path, oldText, newText string) []diffRow {
	rows := parseUnifiedRows(stripDiffNoNewline(udiff.Unified("a/"+path, "b/"+path, oldText, newText)))
	emphasizeDiffRows(rows)
	applyDiffSyntax(rows, path, oldText, newText)
	return rows
}

func writeDiffRows(path, content string) []diffRow {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	rows := make([]diffRow, 0, len(lines))
	for i, line := range lines {
		rows = append(rows, diffRow{newNum: i + 1, marker: '+', text: line})
	}
	applyDiffSyntax(rows, path, "", content)
	return rows
}

func applyDiffSyntax(rows []diffRow, path, oldText, newText string) {
	oldLines := highlightedSourceLines(path, oldText)
	newLines := highlightedSourceLines(path, newText)
	for i := range rows {
		var syntax []diffSegment
		switch rows[i].marker {
		case '-':
			syntax = sourceLine(oldLines, rows[i].oldNum)
		case '+', ' ':
			syntax = sourceLine(newLines, rows[i].newNum)
		}
		if len(syntax) > 0 {
			rows[i].segments = mergeDiffEmphasis(rows[i].segments, syntax)
		}
	}
}

func sourceLine(lines [][]diffSegment, number int) []diffSegment {
	if number <= 0 || number > len(lines) {
		return nil
	}
	return lines[number-1]
}

func highlightedSourceLines(path, source string) [][]diffSegment {
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Analyse(source)
	}
	if lexer == nil {
		return nil
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, source)
	if err != nil {
		return nil
	}
	lines := make([][]diffSegment, 1, strings.Count(source, "\n")+1)
	for token := iterator(); token != chroma.EOF; token = iterator() {
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if part != "" {
				line := len(lines) - 1
				lines[line] = append(lines[line], diffSegment{text: part, fg: syntaxTokenColor(token.Type)})
			}
			if i < len(parts)-1 {
				lines = append(lines, nil)
			}
		}
	}
	return lines
}

func syntaxTokenColor(token chroma.TokenType) color.Color {
	switch {
	case token == chroma.NameBuiltin || token == chroma.NameBuiltinPseudo:
		return everforest.Aqua
	case token == chroma.NameFunction || token == chroma.NameFunctionMagic:
		return everforest.Green
	case token.InCategory(chroma.Comment):
		return everforest.Muted
	case token.InCategory(chroma.Keyword):
		return everforest.Purple
	case token.InSubCategory(chroma.LiteralString):
		return everforest.Green
	case token.InSubCategory(chroma.LiteralNumber):
		return everforest.Purple
	case token.InCategory(chroma.Operator):
		return everforest.Red
	case token.InCategory(chroma.Punctuation):
		return everforest.Muted
	default:
		return everforest.Text
	}
}

func mergeDiffEmphasis(emphasis, syntax []diffSegment) []diffSegment {
	if len(emphasis) == 0 {
		return syntax
	}
	flags := make([]bool, 0)
	for _, segment := range emphasis {
		for range segment.text {
			flags = append(flags, segment.emph)
		}
	}
	merged := make([]diffSegment, 0, len(syntax))
	position := 0
	for _, segment := range syntax {
		for _, r := range segment.text {
			emph := position < len(flags) && flags[position]
			position++
			if len(merged) > 0 && merged[len(merged)-1].emph == emph && sameColor(merged[len(merged)-1].fg, segment.fg) {
				merged[len(merged)-1].text += string(r)
			} else {
				merged = append(merged, diffSegment{text: string(r), emph: emph, fg: segment.fg})
			}
		}
	}
	return merged
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func parseUnifiedRows(diff string) []diffRow {
	rows := make([]diffRow, 0)
	oldN, newN := 0, 0
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			oldN, newN = parseHunkHeader(line)
			rows = append(rows, diffRow{marker: '@', text: line})
		case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "-"):
			rows = append(rows, diffRow{oldNum: oldN, marker: '-', text: line[1:]})
			oldN++
		case strings.HasPrefix(line, "+"):
			rows = append(rows, diffRow{newNum: newN, marker: '+', text: line[1:]})
			newN++
		case strings.HasPrefix(line, " "):
			rows = append(rows, diffRow{oldNum: oldN, newNum: newN, marker: ' ', text: line[1:]})
			oldN++
			newN++
		}
	}
	return rows
}

func parseHunkHeader(line string) (int, int) {
	line = strings.TrimPrefix(line, "@@ ")
	if idx := strings.Index(line, " @@"); idx >= 0 {
		line = line[:idx]
	}
	parts := strings.Fields(line)
	if len(parts) != 2 {
		return 0, 0
	}
	return parseHunkRange(parts[0]), parseHunkRange(parts[1])
}

func parseHunkRange(field string) int {
	field = strings.TrimLeft(field, "-+")
	if idx := strings.IndexByte(field, ','); idx >= 0 {
		field = field[:idx]
	}
	n, _ := strconv.Atoi(field)
	return n
}

func diffGutterWidths(rows []diffRow) (int, int) {
	oldWidth, newWidth := 1, 1
	for _, row := range rows {
		if row.oldNum > 0 {
			oldWidth = max(oldWidth, len(strconv.Itoa(row.oldNum)))
		}
		if row.newNum > 0 {
			newWidth = max(newWidth, len(strconv.Itoa(row.newNum)))
		}
	}
	return oldWidth, newWidth
}

func emphasizeDiffRows(rows []diffRow) {
	for i := 0; i < len(rows); {
		if rows[i].marker != '-' {
			i++
			continue
		}
		delStart := i
		for i < len(rows) && rows[i].marker == '-' {
			i++
		}
		delEnd := i
		addStart := i
		for i < len(rows) && rows[i].marker == '+' {
			i++
		}
		addEnd := i

		pairs := min(delEnd-delStart, addEnd-addStart)
		for k := range pairs {
			oldSegments, newSegments := wordDiffSegments(rows[delStart+k].text, rows[addStart+k].text)
			rows[delStart+k].segments = oldSegments
			rows[addStart+k].segments = newSegments
		}
	}
}

func wordDiffSegments(oldLine, newLine string) ([]diffSegment, []diffSegment) {
	diffs := diffmatchpatch.New().DiffMain(oldLine, newLine, false)
	oldSegments := make([]diffSegment, 0, len(diffs))
	newSegments := make([]diffSegment, 0, len(diffs))
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			oldSegments = append(oldSegments, diffSegment{text: d.Text})
			newSegments = append(newSegments, diffSegment{text: d.Text})
		case diffmatchpatch.DiffDelete:
			oldSegments = append(oldSegments, diffSegment{text: d.Text, emph: true})
		case diffmatchpatch.DiffInsert:
			newSegments = append(newSegments, diffSegment{text: d.Text, emph: true})
		}
	}
	return oldSegments, newSegments
}

// renderDiffRow returns one rendered visual line per wrapped segment. Long
// source lines are wrapped to codeWidth so every visual line is exactly the
// same width; the gutter is only printed on the first visual line.
func renderDiffRow(row diffRow, oldWidth, newWidth, codeWidth int, styles harnessStyles) []string {
	gutterWidth := oldWidth + newWidth + 4
	if row.marker == '@' {
		return []string{lipgloss.NewStyle().Foreground(everforest.Blue).Width(gutterWidth + codeWidth).Render(row.text)}
	}
	oldStr, newStr := "", ""
	if row.oldNum > 0 {
		oldStr = strconv.Itoa(row.oldNum)
	}
	if row.newNum > 0 {
		newStr = strconv.Itoa(row.newNum)
	}
	gutter := fmt.Sprintf("%*s %*s %c ", oldWidth, oldStr, newWidth, newStr, row.marker)

	bg := everforest.Background
	emphFg := everforest.Text
	switch row.marker {
	case '-':
		bg, emphFg = everforest.DiffDeleteBg, everforest.Red
	case '+':
		bg, emphFg = everforest.DiffInsertBg, everforest.Green
	}
	gutterStyle := lipgloss.NewStyle().Foreground(everforest.Muted).Background(bg)
	blankGutter := gutterStyle.Render(strings.Repeat(" ", gutterWidth))

	visual := wrapDiffSegments(row, codeWidth)
	lines := make([]string, 0, len(visual))
	for i, segments := range visual {
		g := gutter
		if i > 0 {
			g = blankGutter
		}
		lines = append(lines, gutterStyle.Render(g)+renderDiffSegments(segments, everforest.Text, emphFg, bg, codeWidth))
	}
	return lines
}

// wrapDiffSegments splits a row into visual lines no wider than width, keeping
// word-level emphasis intact across the wrap.
func wrapDiffSegments(row diffRow, width int) [][]diffSegment {
	if len(row.segments) == 0 {
		return wrapPlainSegment(row.text, width)
	}
	visual := make([][]diffSegment, 0, 1)
	current := make([]diffSegment, 0, len(row.segments))
	currentWidth := 0
	appendRune := func(r rune, emph bool, fg color.Color) {
		if len(current) > 0 && current[len(current)-1].emph == emph && sameColor(current[len(current)-1].fg, fg) {
			current[len(current)-1].text += string(r)
			return
		}
		current = append(current, diffSegment{text: string(r), emph: emph, fg: fg})
	}
	for _, segment := range row.segments {
		for _, r := range segment.text {
			runeWidth := lipgloss.Width(string(r))
			if currentWidth+runeWidth > width && currentWidth > 0 {
				visual = append(visual, current)
				current = make([]diffSegment, 0, len(row.segments))
				currentWidth = 0
			}
			appendRune(r, segment.emph, segment.fg)
			currentWidth += runeWidth
		}
	}
	if len(current) > 0 || len(visual) == 0 {
		visual = append(visual, current)
	}
	return visual
}

func wrapPlainSegment(text string, width int) [][]diffSegment {
	if text == "" {
		return [][]diffSegment{{{}}}
	}
	visual := make([][]diffSegment, 0, 1)
	var current strings.Builder
	currentWidth := 0
	for _, r := range text {
		runeWidth := lipgloss.Width(string(r))
		if currentWidth+runeWidth > width && currentWidth > 0 {
			visual = append(visual, []diffSegment{{text: current.String()}})
			current.Reset()
			currentWidth = 0
		}
		current.WriteRune(r)
		currentWidth += runeWidth
	}
	visual = append(visual, []diffSegment{{text: current.String()}})
	return visual
}

func renderDiffSegments(segments []diffSegment, fg, emphFg, bg color.Color, width int) string {
	base := lipgloss.NewStyle().Foreground(fg).Background(bg)
	emph := lipgloss.NewStyle().Foreground(emphFg).Background(bg).Bold(true)
	var b strings.Builder
	used := 0
	for _, segment := range segments {
		style := base
		switch {
		case segment.emph:
			style = emph
		case segment.fg != nil:
			style = lipgloss.NewStyle().Foreground(segment.fg).Background(bg)
		}
		b.WriteString(style.Render(segment.text))
		used += lipgloss.Width(segment.text)
	}
	if pad := width - used; pad > 0 {
		b.WriteString(base.Render(strings.Repeat(" ", pad)))
	}
	return b.String()
}

func (m *chatModel) renderMarkdown(markdown string, width int) string {
	return m.renderStyledMarkdown(markdown, width, everforestMarkdownStyle(), m.styles.assistant)
}

func (m *chatModel) renderThinkingMarkdown(markdown string, width int) string {
	return m.renderStyledMarkdown(markdown, width, everforestThinkingMarkdownStyle(), m.styles.thinking)
}

func (m *chatModel) renderStyledMarkdown(markdown string, width int, style ansi.StyleConfig, fallback lipgloss.Style) string {
	markdown = normalizeMarkdownMath(markdown)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return fallback.Width(width).Render(markdown)
	}
	output, err := renderer.Render(markdown)
	if err != nil {
		return fallback.Width(width).Render(markdown)
	}
	return output
}

// spinnerFrames sweeps a bar up and down. Every frame is one cell wide and from
// the block family, so the sweep keeps its shape in any font that carries the
// rest of the UI, and it never reads as a status glyph.
var spinnerFrames = [...]string{"▁", "▃", "▄", "▅", "▆", "▇", "█", "▇", "▆", "▅", "▄", "▃", "▁"}

// spinnerFrame is the sweep position for a turn that began at start. The frames
// advance with the animation tick, so the sweep is as smooth as the redraw.
func spinnerFrame(start time.Time) string {
	return spinnerFrames[time.Since(start).Milliseconds()/spinnerFrameMillis%int64(len(spinnerFrames))]
}

// spinnerFrameMillis is how long one sweep frame holds. The animation tick is
// what actually advances it, so a shorter hold than the tick only wastes frames.
const spinnerFrameMillis = 200

func animationTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return animationTickMsg{} })
}

func cancelArmTimeoutCmd(id uint64) tea.Cmd {
	return tea.Tick(cancelArmTimeout, func(time.Time) tea.Msg { return cancelArmTimeoutMsg{id: id} })
}

// applySessionAction updates the list in place for a close or a delete, so the
// row the user was working on stays under the cursor.
func (m *chatModel) applySessionAction(msg sessionActionedMsg) {
	if m.modal == nil || m.modal.kind != modalSessions {
		return
	}
	index := -1
	for i, option := range m.modal.options {
		if option.session == msg.id {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}

	if msg.action == "close" {
		if !sessionConcluded(m.modal.options[index].sessionStatus) {
			m.modal.options[index].sessionStatus = contracts.SessionTombstone
		}
		return
	}

	// The deleted session may be the one this client is attached to, and the
	// server no longer has it: detach rather than let the next send fail.
	if msg.id == m.session.ID {
		m.session = store.SessionMeta{}
		m.client.SetSession(uuid.Nil)
	}
	m.modal.options = append(m.modal.options[:index], m.modal.options[index+1:]...)
	numberSessionOptions(m.modal.options)
	if m.modal.selected > index {
		m.modal.selected--
	}
	m.modal.selected = clamp(m.modal.selected, 0, len(m.modal.options)-1)
}

// sessionConcluded reports whether the session has ended, which is the group
// the contract calls "concluded states". Closing a session that ended keeps the
// conclusion it recorded, so the list does not change; one that has not ended
// records an inactive conclusion, which is the change worth showing.
func sessionConcluded(status contracts.SessionStatus) bool {
	switch status {
	case contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone:
		return true
	default:
		return false
	}
}

func (m *chatModel) showSessions(sessions []contracts.SessionSummary, required bool) {
	options := []modalOption{{label: "Start a new session", shortcut: 'n'}}
	for _, session := range sessions {
		status := session.Status
		if status == "" {
			status = contracts.SessionIdle
		}
		name := session.Name
		if name == "" {
			name = "Untitled session"
		}
		parts := []string{orModel(session.Model), humanTime(session.UpdatedAt)}
		if session.Cwd != "" {
			parts = append(parts, session.Cwd)
		}
		option := modalOption{
			label:         name,
			detailParts:   parts,
			session:       session.ID,
			sessionStatus: status,
			sessionActive: session.Active,
			agentKind:     session.AgentKind,
		}
		options = append(options, option)
	}
	numberSessionOptions(options)
	m.modal = &modalModel{kind: modalSessions, options: options, required: required}
}

// numberSessionOptions gives the new-session row its "n" and each session its
// number key. The list is renumbered after a row is deleted, so a number key
// always names the row it is shown against.
func numberSessionOptions(options []modalOption) {
	for i := range options {
		switch {
		case i == 0:
			options[i].shortcut = 'n'
		case i <= 9:
			options[i].shortcut = rune('0' + i)
		default:
			options[i].shortcut = 0
		}
	}
}

func (m *chatModel) showModels(models []glue.ListModelOutput, switching bool) {
	var options []modalOption
	for _, model := range models {
		options = append(options, modalOption{
			label:    fmt.Sprintf("%s (%s) @ %d", model.ModelName, model.ProviderName, model.ContextWindow),
			provider: model.ProviderName,
			model:    model.ModelID,
		})
	}
	if len(options) == 0 {
		m.showNotice("No models configured", "Add a provider and model to models.json, then restart the server.", false)
		return
	}
	m.modal = &modalModel{kind: modalModels, title: "Choose a model", body: "The model is applied to this session.", options: options, switching: switching}
}

func prettyToolArguments(arguments string) string {
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return arguments
	}
	formatted, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return arguments
	}
	return string(formatted)
}

func numberedContent(content string) string {
	lines := strings.Split(content, "\n")
	width := len(fmt.Sprintf("%d", len(lines)))
	for i := range lines {
		lines[i] = fmt.Sprintf("%*d  %s", width, i+1, lines[i])
	}
	return strings.Join(lines, "\n")
}

func stripDiffNoNewline(diff string) string {
	lines := strings.Split(diff, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) == `\ No newline at end of file` {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

// toolCallPreview renders a decoded, readable body for a tool call in the
// transcript. The tool name is kept separate by the renderer, so the returned
// text never repeats it. Unknown tools fall back to pretty-printed arguments.
func toolCallPreview(name, arguments string) string {
	switch contracts.ToolType(name) {
	case contracts.ReadTool:
		preview, ok := readToolCallPreview(arguments)
		if !ok {
			return prettyToolArguments(arguments)
		}
		return preview

	case contracts.BashTool:
		var args struct {
			Command string `json:"command"`
			Workdir string `json:"workdir"`
			Timeout *int   `json:"timeout"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return prettyToolArguments(arguments)
		}
		var lines []string
		if args.Workdir != "" {
			lines = append(lines, "cwd  "+args.Workdir)
		} else if args.Timeout != nil {
			lines = append(lines, fmt.Sprintf("timeout  %ds", *args.Timeout))
		}
		lines = append(lines, "$ "+args.Command)
		return strings.Join(lines, "\n")

	case contracts.WriteTool:
		var args struct {
			Path    string  `json:"path"`
			Content *string `json:"content"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return prettyToolArguments(arguments)
		}
		content := "<missing content>"
		lineCount, byteCount := 0, 0
		if args.Content != nil {
			content = *args.Content
			byteCount = len([]byte(content))
			if content != "" {
				lineCount = strings.Count(content, "\n") + 1
			}
		}
		return fmt.Sprintf("→ %s  (%d lines, %d bytes)\n%s", args.Path, lineCount, byteCount, numberedContent(content))

	case contracts.EditTool:
		var args struct {
			Path       string  `json:"path"`
			OldString  *string `json:"old_string"`
			NewString  *string `json:"new_string"`
			ReplaceAll bool    `json:"replace_all"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return prettyToolArguments(arguments)
		}
		oldText, newText := "<missing old text>", "<missing new text>"
		if args.OldString != nil {
			oldText = *args.OldString
		}
		if args.NewString != nil {
			newText = *args.NewString
		}
		mode := ""
		if args.ReplaceAll {
			mode = " (every match)"
		}
		diff := udiff.Unified("a/"+args.Path, "b/"+args.Path, oldText, newText)
		diff = stripDiffNoNewline(diff)
		return fmt.Sprintf("diff --git a/%s b/%s%s\n%s", args.Path, args.Path, mode, strings.TrimRight(diff, "\n"))
	}

	return prettyToolArguments(arguments)
}

func readToolCallPreview(arguments string) (string, bool) {
	var args struct {
		Path     string `json:"path"`
		Offset   *int   `json:"offset"`
		Limit    *int   `json:"limit"`
		Metadata bool   `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", false
	}
	switch {
	case args.Metadata:
		return args.Path + "  (metadata)", true
	case args.Offset != nil && args.Limit != nil:
		return fmt.Sprintf("%s  (lines %d-%d)", args.Path, *args.Offset, *args.Offset+*args.Limit-1), true
	case args.Offset != nil:
		return fmt.Sprintf("%s  (from line %d)", args.Path, *args.Offset), true
	case args.Limit != nil:
		return fmt.Sprintf("%s  (first %d lines)", args.Path, *args.Limit), true
	default:
		return args.Path, true
	}
}

func (m *chatModel) showTimeline(view *TimelineView) {
	rows := timelineTreeRows(view.Events)
	options := make([]modalOption, 0, len(rows))
	selected := 0
	for i := range rows {
		event := rows[i].event
		displays := timelineEventDisplays(event)
		for displayIndex, display := range displays {
			isHeadRow := event.ID == view.Head && displayIndex == len(displays)-1
			tree, fork := rows[i].prefix, rows[i].fork
			if displayIndex > 0 {
				tree = rows[i].subprefix + strings.Repeat(" ", lipgloss.Width(timelineForkLabel(fork)))
				fork = ""
			}
			options = append(options, modalOption{
				label:   display.text,
				role:    display.role,
				current: isHeadRow,
				tree:    tree,
				fork:    fork,
				event:   &event,
			})
			if isHeadRow {
				selected = len(options) - 1
			}
		}
	}
	if len(options) == 0 {
		m.showNotice("Timeline is empty", "There is no event to branch from yet.", false)
		return
	}
	m.modal = &modalModel{
		kind: modalTimeline, title: "Branch timeline",
		body:    "* marks the current event. ⎇ marks a fork.",
		options: options, selected: selected,
	}
}

func (m *chatModel) showNotice(title, body string, required bool) {
	m.modal = &modalModel{kind: modalNotice, title: title, body: body, required: required, options: []modalOption{{label: "OK", shortcut: 'o'}}}
}

func loadingModal(label string) *modalModel {
	return &modalModel{kind: modalNotice, title: label, body: "Please wait...", required: true}
}

// isContentDelta reports whether an event carries a piece of the model's own
// output: the one kind of event that arrives token by token.
func isContentDelta(kind contracts.EventStreamKind) bool {
	return kind == contracts.DeltaContent || kind == contracts.DeltaReasoning
}

func (m *chatModel) appendDelta(kind contracts.EventStreamKind, text string) {
	role := "assistant"
	switch kind {
	case contracts.DeltaReasoning:
		role = "thinking"
	case contracts.EventProviderEvent:
		m.stopStreaming()
		role, text = "status", statusLabel(text)
	case contracts.EventCompactionExecuted:
		m.stopStreaming()
		role = "compaction"
	case contracts.EventManualCompactionTriggered, contracts.EventAutoCompactionTriggered:
		m.stopStreaming()
		role, text = "status", statusLabel(text)
	case contracts.EventToolCall:
		m.stopStreaming()
		m.appendToolEvent("call", text)
		return
	case contracts.EventToolResult:
		m.stopStreaming()
		m.appendToolEvent("result", text)
		return
	case contracts.EventSkillLoad:
		m.stopStreaming()
		m.blocks = append(m.blocks, block{role: "skill", text: text})
		return
	case contracts.EventToolTaskCheckList:
		// Checklist state is rendered in the header, never as transcript text.
		return
	}
	streaming := isContentDelta(kind)
	if streaming && m.streaming && m.streamingAt == len(m.blocks)-1 && m.blocks[m.streamingAt].role == role {
		entry := &m.blocks[len(m.blocks)-1]
		entry.text += text
		entry.renderedValid = false
		return
	}
	if role == "status" && len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].role == role {
		entry := &m.blocks[len(m.blocks)-1]
		entry.text = text
		entry.renderedValid = false
		return
	}
	m.blocks = append(m.blocks, block{role: role, text: text})
	if streaming {
		// A new block takes over the stream: the one that was streaming is
		// complete, so it gets its markdown render.
		m.stopStreaming()
		m.streaming = true
		m.streamingAt = len(m.blocks) - 1
	}
}

// stopStreaming ends the block in flight. A block is rendered as plain text
// while it streams — markdown waits for the whole message — so its cached
// render has to be dropped for the final pass.
func (m *chatModel) stopStreaming() {
	if !m.streaming {
		return
	}
	m.streaming = false
	if m.streamingAt >= 0 && m.streamingAt < len(m.blocks) {
		m.blocks[m.streamingAt].renderedValid = false
	}
}

func (m *chatModel) appendToolEvent(kind, text string) {
	name := ""
	if fields := strings.Fields(text); len(fields) > 0 {
		name = fields[0]
	}
	if isChecklistTool(name) {
		return
	}
	display, arguments := text, ""
	if kind == "call" {
		arguments = strings.TrimSpace(strings.TrimPrefix(text, name))
		display = toolCallPreview(name, arguments)
	}
	status := ""
	if kind == "result" {
		if parsedName, parsedStatus, output, resultErr, ok := parseToolResultEvent(text); ok {
			name, status = parsedName, parsedStatus
			if output != "" || resultErr != "" {
				display = transcriptToolResultDisplay(name, status, output, resultErr)
			}
		} else {
			status = string(contracts.ToolExecutionError)
		}
	}
	m.blocks = append(m.blocks, block{role: "tool", text: display, toolKind: kind, toolStatus: status, toolName: name, toolArgs: arguments})
}

func parseToolResultEvent(text string) (name, status, output, resultErr string, ok bool) {
	header, rest, hasOutput := strings.Cut(text, "\n")
	open := strings.Index(header, " [")
	if open <= 0 {
		return "", "", "", "", false
	}
	end := strings.IndexByte(header[open+2:], ']')
	if end < 0 {
		return "", "", "", "", false
	}
	end += open + 2
	name = header[:open]
	status = header[open+2 : end]
	if tail := strings.TrimSpace(header[end+1:]); strings.HasPrefix(tail, ":") {
		resultErr = strings.TrimSpace(strings.TrimPrefix(tail, ":"))
	}
	if hasOutput {
		output = rest
	}
	return name, status, output, resultErr, true
}

func (m *chatModel) appendError(err error) {
	m.blocks = append(m.blocks, block{role: "error", text: err.Error()})
}

func loadSessionCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		meta, err := client.LoadSession(ctx, id)
		if err != nil {
			return sessionLoadedMsg{err: err}
		}
		_, records, err := client.GetSession(ctx, id)
		return sessionLoadedMsg{output: meta, records: records, err: err}
	}
}

func listSessionsCmd(ctx context.Context, client Client) tea.Cmd {
	return func() tea.Msg {
		sessions, err := client.ListSessions(ctx)
		return sessionsListedMsg{sessions: sessions, err: err}
	}
}

// sessionActionedMsg reports the outcome of a close or a delete, named so the
// screen can say which one failed.
type sessionActionedMsg struct {
	action string
	id     uuid.UUID
	err    error
}

func closeSessionCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		return sessionActionedMsg{action: "close", id: id, err: client.CloseSession(ctx, id)}
	}
}

func deleteSessionCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		return sessionActionedMsg{action: "delete", id: id, err: client.DeleteSession(ctx, id)}
	}
}

func renameSessionCmd(ctx context.Context, client Client, id uuid.UUID, name string) tea.Cmd {
	return func() tea.Msg {
		meta, err := client.RenameSession(ctx, id, name)
		return renamedMsg{meta: meta, err: err}
	}
}

func listProvidersCmd(ctx context.Context, client Client, switching bool) tea.Cmd {
	return func() tea.Msg {
		providers, err := client.ListAllProviderModels(ctx)
		return providersListedMsg{providers: providers, switching: switching, err: err}
	}
}

func createSessionCmd(ctx context.Context, client Client, provider, model string) tea.Cmd {
	return func() tea.Msg {
		cwd, _ := os.Getwd()
		meta, err := client.CreateSession(ctx, SessionCreateOptions{Provider: provider, Model: model, Cwd: cwd})
		return sessionCreatedMsg{output: meta, err: err}
	}
}

func setModelCmd(ctx context.Context, client Client, provider, model string) tea.Cmd {
	return func() tea.Msg {
		output, err := client.SetSessionModel(ctx, provider, model)
		return modelSetMsg{output: output, err: err}
	}
}

func compactCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		meta, err := client.Compact(ctx)
		if err != nil {
			return compactedMsg{err: err}
		}
		_, records, err := client.GetSession(ctx, id)
		return compactedMsg{meta: meta, records: records, err: err}
	}
}

func loadTimelineCmd(ctx context.Context, client Client, id uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		view, err := client.GetTimeline(ctx, id)
		return timelineLoadedMsg{view: view, err: err}
	}
}

func selectBranchCmd(ctx context.Context, client Client, sessionID uuid.UUID, event TimelineEvent) tea.Cmd {
	return func() tea.Msg {
		checklist, err := client.SelectBranch(ctx, sessionID, event.ID)
		return branchSelectedMsg{event: event, checklist: checklist, err: err}
	}
}

func blocksFromRecords(records []store.Record) []block {
	var blocks []block
	skillCallIDs := make(map[string]struct{})
	toolCallNames := make(map[string]string)
	for _, record := range records {
		if record.Message == nil || record.Message.Role != "assistant" {
			continue
		}
		for _, call := range record.Message.ToolCalls {
			typename := string(call.Function.Name)
			toolCallNames[call.ID] = typename
			if isSkillTool(typename) {
				skillCallIDs[call.ID] = struct{}{}
			}
		}
	}
	for _, record := range records {
		switch record.Kind {
		case store.KindCompaction:
			if record.Compaction != nil {
				blocks = append(blocks, block{role: "compaction", text: record.Compaction.Summary})
			}
		case store.KindMessage:
			if record.Message == nil {
				continue
			}
			message := record.Message
			switch message.Role {
			case "user":
				blocks = append(blocks, block{role: "user", text: message.Text(), imageCount: len(message.Images)})
			case "assistant":
				if message.ReasoningContent != "" {
					blocks = append(blocks, block{role: "thinking", text: message.ReasoningContent})
				}
				if message.Text() != "" {
					blocks = append(blocks, block{role: "assistant", text: message.Text()})
				}
				for _, call := range message.ToolCalls {
					toolName := string(call.Function.Name)
					if isSkillTool(toolName) {
						blocks = append(blocks, block{role: "skill", text: "Skill loaded: " + skillNameFromCall(call)})
						continue
					}
					if isChecklistTool(toolName) {
						continue
					}
					blocks = append(blocks, block{role: "tool", text: toolCallPreview(toolName, call.Function.Arguments), toolKind: "call", toolName: toolName, toolArgs: call.Function.Arguments})
				}
			case "tool":
				toolName := toolCallNames[message.ToolCallID]
				if _, skill := skillCallIDs[message.ToolCallID]; !skill && !isChecklistTool(toolName) {
					status := string(message.Status)
					if status == "" {
						status = string(contracts.ToolExecutionSuccess)
					}
					blocks = append(blocks, block{role: "tool", text: transcriptToolResultDisplay(toolName, status, message.Text(), ""), toolKind: "result", toolStatus: status, toolName: toolName})
				}
			}
		}
	}
	return blocks
}

func skillNameFromCall(call contracts.ToolCall) string {
	var args struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args.Name != "" {
		return args.Name
	}
	return string(contracts.ReadSkillTool)
}

func isSkillTool(name string) bool { return name == string(contracts.ReadSkillTool) }

func isChecklistTool(name string) bool { return name == string(contracts.TaskChecklistTool) }

func decodeTaskChecklist(text string) (contracts.TaskChecklistState, error) {
	var state contracts.TaskChecklistState
	err := json.Unmarshal([]byte(text), &state)
	return state, err
}

func taskChecklistFromRecords(records []store.Record) contracts.TaskChecklistState {
	state := contracts.TaskChecklistState{}
	toolNames := make(map[string]string)
	for _, record := range records {
		if record.Message != nil && record.Message.Role == "assistant" {
			for _, call := range record.Message.ToolCalls {
				toolNames[call.ID] = string(call.Function.Name)
			}
		}
	}
	for _, record := range records {
		if record.Compaction != nil && record.Compaction.TaskChecklist != nil {
			state = *record.Compaction.TaskChecklist
		}
		if record.Message != nil && record.Message.Role == "tool" && isChecklistTool(toolNames[record.Message.ToolCallID]) {
			if next, err := decodeTaskChecklist(record.Message.Text()); err == nil {
				state = next
			}
		}
	}
	return state
}

func truncateLine(value string, width int) string {
	if width < 2 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	return string(runes[:min(len(runes), width-1)]) + "…"
}

func toolCallDisplay(call contracts.ToolCall) string {
	if call.Function.Arguments == "" {
		return string(call.Function.Name)
	}
	return string(call.Function.Name) + " " + call.Function.Arguments
}

func toolResultDisplay(status, output, resultErr string) string {
	if resultErr != "" {
		return status + ": " + resultErr
	}
	if output != "" {
		return status + "\n" + output
	}
	return status
}

func transcriptToolResultDisplay(name, status, output, resultErr string) string {
	if name == string(contracts.BashTool) && resultErr == "" {
		var result struct {
			ExitCode  int    `json:"exit_code"`
			Output    string `json:"output"`
			Truncated bool   `json:"truncated"`
			TimedOut  bool   `json:"timed_out"`
		}
		if json.Unmarshal([]byte(output), &result) == nil {
			summary := fmt.Sprintf("%s · exit %d", status, result.ExitCode)
			if result.TimedOut {
				summary += " · timed out"
			}
			if result.Truncated {
				summary += " · output truncated"
			}
			if result.Output != "" {
				return summary + "\n" + result.Output
			}
			return summary
		}
	}
	if name != string(contracts.ReadTool) || status != string(contracts.ToolExecutionSuccess) || resultErr != "" {
		return toolResultDisplay(status, output, resultErr)
	}
	lines := 0
	if output != "" {
		lines = strings.Count(output, "\n")
		if !strings.HasSuffix(output, "\n") {
			lines++
		}
	}
	lineLabel := "lines"
	if lines == 1 {
		lineLabel = "line"
	}
	return fmt.Sprintf("%s · %d %s, %d bytes", status, lines, lineLabel, len([]byte(output)))
}

type timelineTreeRow struct {
	event     TimelineEvent
	prefix    string
	subprefix string
	fork      string
}

func timelineTreeRows(events []TimelineEvent) []timelineTreeRow {
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
	rows := make([]timelineTreeRow, 0, len(events))
	seen := make(map[uuid.UUID]struct{}, len(events))
	var visitEvent func(TimelineEvent, string, string, string, string)
	visitEvent = func(event TimelineEvent, indent, marker, continuationIndent, fork string) {
		if _, exists := seen[event.ID]; exists {
			return
		}
		seen[event.ID] = struct{}{}
		rows = append(rows, timelineTreeRow{
			event: event, prefix: indent + marker,
			subprefix: indent + timelineSubrowMarker(marker), fork: fork,
		})

		next := children[event.ID]
		if len(next) == 1 {
			visitEvent(next[0], continuationIndent, "│ ", continuationIndent, "")
			return
		}
		for i, child := range next {
			last := i == len(next)-1
			childMarker, guide := "├─ ", "│  "
			if last {
				childMarker, guide = "└─ ", "   "
			}
			fork := "original"
			if child.BranchFrom != nil {
				fork = "branch"
			}
			visitEvent(child, continuationIndent, childMarker, continuationIndent+guide, fork)
		}
	}
	rootEvents := children[uuid.Nil]
	for i, event := range rootEvents {
		if len(rootEvents) == 1 {
			visitEvent(event, "", "● ", "", "")
			continue
		}
		last := i == len(rootEvents)-1
		marker, guide := "├─ ", "│  "
		if last {
			marker, guide = "└─ ", "   "
		}
		visitEvent(event, "", marker, guide, "")
	}
	for _, event := range events {
		if _, exists := seen[event.ID]; !exists {
			visitEvent(event, "", "● ", "", "")
		}
	}
	return rows
}

func timelineSubrowMarker(marker string) string {
	switch marker {
	case "● ", "│ ":
		return "│ "
	case "├─ ":
		return "│  "
	case "└─ ":
		return "   "
	default:
		return strings.Repeat(" ", lipgloss.Width(marker))
	}
}

type timelineDisplay struct {
	role string
	text string
}

func timelineEventDisplays(event TimelineEvent) []timelineDisplay {
	if event.Record == nil {
		return previewDisplays(event.Preview)
	}
	if event.Record.Message != nil {
		message := event.Record.Message
		if message.Role == "user" {
			return []timelineDisplay{{role: "user", text: singleLine(message.Text())}}
		}
		if message.Role == "tool" {
			if _, err := decodeTaskChecklist(message.Text()); err == nil {
				return []timelineDisplay{{role: "system", text: "task checklist updated"}}
			}
			return []timelineDisplay{{role: "tool_result", text: singleLine(message.Text())}}
		}
		displays := make([]timelineDisplay, 0, 2+len(message.ToolCalls))
		if message.ReasoningContent != "" {
			displays = append(displays, timelineDisplay{role: "thinking", text: singleLine(message.ReasoningContent)})
		}
		if message.Text() != "" {
			displays = append(displays, timelineDisplay{role: "assistant", text: singleLine(message.Text())})
		}
		for _, call := range message.ToolCalls {
			toolName := string(call.Function.Name)
			if isSkillTool(toolName) {
				displays = append(displays, timelineDisplay{role: "skill", text: skillNameFromCall(call)})
				continue
			}
			if isChecklistTool(toolName) {
				continue
			}
			displays = append(displays, timelineDisplay{role: "tool_call", text: singleLine(toolCallDisplay(call))})
		}
		if len(displays) == 0 {
			displays = append(displays, timelineDisplay{role: "assistant", text: "empty response"})
		}
		return displays
	}
	if event.Record.Compaction != nil {
		return []timelineDisplay{{role: "assistant", text: "context compacted: " + singleLine(event.Record.Compaction.Summary)}}
	}
	text := singleLine(event.Record.Text)
	if text == "" {
		text = strings.ReplaceAll(string(event.Kind), "_", " ")
	}
	return []timelineDisplay{{role: "assistant", text: text}}
}

func previewDisplays(preview *store.EventPreview) []timelineDisplay {
	if preview == nil {
		return []timelineDisplay{{role: "assistant", text: "content unavailable"}}
	}
	role := preview.Role
	if role == "" {
		role = "assistant"
	}
	text := preview.Text
	if text == "" {
		text = "content unavailable"
	}
	if preview.ImageCount > 0 {
		text = strings.TrimSpace(text + " " + imageBadgeText(preview.ImageCount))
	}
	return []timelineDisplay{{role: role, text: text}}
}

func imageBadgeText(count int) string {
	badges := make([]string, 0, count)
	for i := range count {
		badges = append(badges, fmt.Sprintf("[Image %d]", i+1))
	}
	return strings.Join(badges, " ")
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
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
	return fmt.Sprintf("Branch selected at %q", preview)
}
