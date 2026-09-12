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
	skillName     string
	rendered      string
	renderedWidth int
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
	composer         textarea.Model
	checklist        contracts.TaskChecklistState
	modal            *modalModel
	commandMenu      bool
	commandSelection int

	working      bool
	workBegan    time.Time
	workStatus   string
	cancelArmed  bool
	cancelArmID  uint64
	cancelStatus string
	turnCancel   context.CancelFunc
	stream       chan tea.Msg
	initCmd      tea.Cmd
	streaming    bool
	streamingAt  int
	streamTick   bool
	streamTickID uint64
	streamDirty  bool
}

type streamDeltaMsg struct {
	kind contracts.DeltaKind
	text string
}

type clipboardImageMsg struct {
	image contracts.ImageInput
	err   error
}

type streamApprovalMsg struct{ update ApprovalUpdate }
type turnDoneMsg struct {
	reply *ChatReply
	err   error
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
type streamRefreshTickMsg struct{ id uint64 }

const cancelArmTimeout = 10 * time.Second
const streamRefreshInterval = time.Second

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
	input.Prompt = "λ "
	input.Placeholder = "Ask, plan, build..."
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.MaxContentHeight = 200
	input.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	input.SetVirtualCursor(true)
	styleTextarea(&input)

	view := viewport.New()
	view.SoftWrap = false
	view.FillHeight = true
	view.MouseWheelEnabled = true
	view.MouseWheelDelta = 3
	view.Style = lipgloss.NewStyle().Padding(0, 1)

	m := &chatModel{
		ctx:       ctx,
		client:    client,
		styles:    newHarnessStyles(),
		viewport:  view,
		composer:  input,
		clipboard: defaultClipboard(),
	}
	if opts.SessionID != uuid.Nil {
		m.modal = loadingModal("Opening session")
		m.initCmd = loadSessionCmd(ctx, client, opts.SessionID)
	} else {
		m.showSessions(sessions, true)
	}
	return m
}

func (m *chatModel) Init() tea.Cmd {
	return tea.Batch(m.composer.Focus(), m.viewport.Init(), m.initCmd)
}

func (m *chatModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scrollApproval(0)
		m.reflow(true)
		m.streamDirty = false
		return m, nil
	case streamDeltaMsg:
		if msg.kind == contracts.DeltaStatus {
			status := statusLabel(msg.text)
			if m.cancelArmed {
				m.cancelStatus = status
			} else {
				m.workStatus = status
			}
		} else if msg.kind == contracts.DeltaTaskChecklist {
			if state, err := decodeTaskChecklist(msg.text); err == nil {
				m.checklist = state
			}
		}
		wasStreaming, wasStreamingAt := m.streaming, m.streamingAt
		m.appendDelta(msg.kind, msg.text)
		wait := waitStream(m.ctx, m.stream)
		if msg.kind == contracts.DeltaContent || msg.kind == contracts.DeltaReasoning {
			if !wasStreaming || wasStreamingAt != m.streamingAt || !m.streamTick {
				m.stopStreamRefresh()
				m.refreshTranscript(true)
				return m, tea.Batch(wait, m.startStreamRefresh())
			}
			m.streamDirty = true
			return m, wait
		}
		if msg.kind != contracts.DeltaTaskChecklist {
			m.stopStreamRefresh()
			m.refreshTranscript(true)
		}
		return m, wait
	case streamApprovalMsg:
		m.handleApprovalUpdate(msg.update)
		return m, waitStream(m.ctx, m.stream)
	case clipboardImageMsg:
		return m.handleClipboardImage(msg)
	case turnDoneMsg:
		m.stopStreamRefresh()
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		m.working = false
		m.streaming = false
		m.cancelArmed = false
		m.cancelStatus = ""
		m.workStatus = ""
		if errors.Is(msg.err, context.Canceled) || msg.reply != nil && msg.reply.Status == "canceled" {
			m.appendDelta(contracts.DeltaStatus, "generation canceled")
		} else if msg.err != nil {
			m.appendError(msg.err)
		} else if msg.reply != nil {
			m.used = msg.reply.ContextTokens
			if msg.reply.Model != "" {
				m.session.Model = msg.reply.Model
			}
			if msg.reply.ContextWindow > 0 {
				m.contextWindow = msg.reply.ContextWindow
			}
			if msg.reply.Name != "" {
				m.session.Name = msg.reply.Name
			}
			if msg.reply.Pending != nil && m.modal == nil {
				m.showApproval(msg.reply.Pending)
			}
		}
		m.refreshTranscript(true)
		m.reflow(false)
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
		m.reflow(false)
		m.client.SetSession(msg.output.ID)
		m.blocks = blocksFromRecords(msg.records)
		m.checklist = taskChecklistFromRecords(msg.records)
		m.modal = nil
		m.refreshTranscript(true)
		return m, nil
	case sessionsListedMsg:
		if msg.err != nil {
			m.showNotice("Could not list sessions", msg.err.Error(), false)
		} else {
			m.showSessions(msg.sessions, false)
		}
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
		m.reflow(false)
		m.client.SetSession(msg.output.ID)
		m.blocks = nil
		m.checklist = contracts.TaskChecklistState{}
		m.used = 0
		m.modal = nil
		m.refreshTranscript(true)
		return m, nil
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
			m.reflow(false)
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
			m.reflow(false)
		}
		return m, nil
	case streamRefreshTickMsg:
		if !m.streamTick || msg.id != m.streamTickID {
			return m, nil
		}
		if !m.streamDirty {
			m.streamTick = false
			return m, nil
		}
		m.streamDirty = false
		m.refreshTranscript(true)
		return m, streamRefreshTickCmd(msg.id)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if m.modal == nil && !m.working {
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			m.updateCommandMenu()
			m.reflow(false)
			return m, cmd
		}
		return m, nil
	case tea.MouseMsg:
		if m.modal != nil && m.modal.kind == modalApproval {
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				m.scrollApproval(-3)
			case tea.MouseWheelDown:
				m.scrollApproval(3)
			}
			return m, nil
		}
		if m.modal == nil {
			mouse := msg.Mouse()
			if len(m.areas) > 1 && (mouse.Y < m.areas[1].y || mouse.Y >= m.areas[1].y+m.areas[1].height) {
				return m, nil
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.reflow(false)
			return m, cmd
		}
		return m, nil
	}

	if m.modal == nil {
		var viewportCmd, composerCmd tea.Cmd
		m.viewport, viewportCmd = m.viewport.Update(message)
		m.composer, composerCmd = m.composer.Update(message)
		m.reflow(false)
		return m, tea.Batch(viewportCmd, composerCmd)
	}
	return m, nil
}

func (m *chatModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.working && key == "esc" {
		if !m.cancelArmed {
			m.cancelArmed = true
			m.cancelArmID++
			m.cancelStatus = m.workStatus
			m.workStatus = "press esc again to cancel"
			m.reflow(false)
			return m, cancelArmTimeoutCmd(m.cancelArmID)
		} else if m.turnCancel != nil {
			m.cancelArmed = false
			m.cancelStatus = ""
			m.workStatus = "canceling"
			m.turnCancel()
		}
		m.reflow(false)
		return m, nil
	}
	if m.modal != nil {
		return m, m.handleModalKey(msg)
	}
	switch msg.Key().Keystroke() {
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
		switch key {
		case "pgup":
			m.viewport.PageUp()
		case "pgdown":
			m.viewport.PageDown()
		case "ctrl+up":
			m.viewport.ScrollUp(3)
		case "ctrl+down":
			m.viewport.ScrollDown(3)
		}
		m.reflow(false)
		return m, nil
	}
	if key == "ctrl+o" {
		m.modal = loadingModal("Loading sessions")
		return m, listSessionsCmd(m.ctx, m.client)
	}
	if key == "ctrl+n" {
		m.modal = loadingModal("Loading models")
		return m, listProvidersCmd(m.ctx, m.client, false)
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
			m.reflow(false)
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
			m.reflow(false)
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
			m.reflow(false)
			return m, nil
		}
	}
	if key == "pgup" {
		m.viewport.PageUp()
		m.reflow(false)
		return m, nil
	}
	if key == "pgdown" {
		m.viewport.PageDown()
		m.reflow(false)
		return m, nil
	}
	if key == "ctrl+up" {
		m.viewport.ScrollUp(3)
		m.reflow(false)
		return m, nil
	}
	if key == "ctrl+down" {
		m.viewport.ScrollDown(3)
		m.reflow(false)
		return m, nil
	}
	if key == "enter" {
		return m, m.submit()
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.updateCommandMenu()
	m.reflow(false)
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
	m.reflow(false)
}

func (m *chatModel) handleModalKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "up", "left", "k", "h":
		m.modal.move(-1)
	case "down", "right", "j", "l", "tab":
		m.modal.move(1)
	case "shift+tab":
		m.modal.move(-1)
	case "pgup":
		if m.modal.kind == modalApproval {
			m.scrollApproval(-8)
		}
	case "pgdown":
		if m.modal.kind == modalApproval {
			m.scrollApproval(8)
		}
	case "ctrl+up":
		if m.modal.kind == modalApproval {
			m.scrollApproval(-1)
		}
	case "ctrl+down":
		if m.modal.kind == modalApproval {
			m.scrollApproval(1)
		}
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

func (m *chatModel) scrollApproval(delta int) {
	if m.modal == nil || m.modal.kind != modalApproval || m.width <= 0 || m.height <= 0 {
		return
	}
	maxOffset := approvalMaxBodyOffset(m.modal, m.width, m.height, m.styles)
	m.modal.bodyOffset = clamp(m.modal.bodyOffset+delta, 0, maxOffset)
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
	case modalApproval:
		approval := modal.approval
		m.modal = nil
		m.blocks = append(m.blocks, block{role: "system", text: "Approval " + option.decision})
		m.refreshTranscript(true)
		return resolveApprovalCmd(m.ctx, m.client, approval, option.decision)
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

func (m *chatModel) submit() tea.Cmd {
	prompt := strings.TrimSpace(m.composer.Value())
	if prompt == "" || m.working {
		return nil
	}
	images := append([]contracts.ImageInput(nil), m.pending...)
	if strings.HasPrefix(prompt, "/") && len(images) == 0 {
		m.composer.Reset()
		m.commandMenu = false
		m.reflow(false)
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
	m.reflow(false)

	input := contracts.UserInput{Text: prompt, Images: images}
	m.blocks = append(m.blocks, block{role: "user", text: prompt, imageCount: len(images)})
	m.working = true
	m.workBegan = time.Now()
	m.workStatus = "working"
	m.cancelArmed = false
	m.refreshTranscript(true)
	m.stream = make(chan tea.Msg, 256)
	stream := m.stream
	thinking := m.thinking
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	emit := func(message tea.Msg) bool {
		select {
		case stream <- message:
			return true
		case <-m.ctx.Done():
			return false
		}
	}
	go func() {
		reply, err := m.client.Chat(turnCtx, input, thinking, func(kind contracts.DeltaKind, text string) {
			emit(streamDeltaMsg{kind: kind, text: text})
		}, func(update ApprovalUpdate) {
			emit(streamApprovalMsg{update: update})
		})
		emit(turnDoneMsg{reply: reply, err: err})
	}()
	// Attachments are consumed only once dispatch has successfully begun.
	m.pending = nil
	return tea.Batch(waitStream(m.ctx, stream), animationTickCmd())
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
	if m.modal != nil || m.working {
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
	m.reflow(false)
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

// attachmentsView shows the staged attachments as [Image N] chips above the
// composer. The composer text itself stays clean.
func (m *chatModel) attachmentsView() string {
	if len(m.pending) == 0 {
		return ""
	}
	return fullWidth(lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1), m.width, m.renderImageBadges(len(m.pending)))
}

// clearAttachments drops every staged image. Ctrl+U is the explicit action
// because the composer carries no marker text to delete.
func (m *chatModel) clearAttachments() {
	if len(m.pending) == 0 {
		return
	}
	m.pending = nil
	m.reflow(false)
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
	commands := m.commandMenuView()
	attachments := m.attachmentsView()
	composer := m.composerView()
	parts := []string{header, m.viewport.View(), status, commands, attachments, composer}
	if len(m.areas) == len(parts) {
		parts[3] = m.commandMenuViewForHeight(m.areas[3].height)
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
	if m.modal != nil && m.modal.kind != modalApproval && m.modal.kind != modalTimeline {
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

func (m *chatModel) reflow(follow bool) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	previousViewportWidth := m.viewport.Width()
	wasAtBottom := m.viewport.AtBottom()
	m.composer.SetWidth(contentWidth(m.styles.composer, m.width))
	header := m.headerView()
	status := m.statusView()
	commands := m.commandMenuView()
	attachments := m.attachmentsView()
	composer := m.composerView()
	m.areas = layoutRows(m.width, m.height, intrinsic(header), fill(), intrinsic(status), intrinsic(commands), intrinsic(attachments), intrinsic(composer))
	main := m.areas[1]
	m.viewport.SetWidth(main.width)
	m.viewport.SetHeight(main.height)
	if previousViewportWidth != main.width {
		m.refreshTranscript(follow || wasAtBottom)
	} else if follow || wasAtBottom {
		m.viewport.GotoBottom()
	}
}

func (m *chatModel) headerView() string {
	content := m.styles.brand.Render("INFAI") + " " + m.styles.headerMeta.Render("HARNESS")
	return fullWidth(m.styles.header, m.width, content)
}

func (m *chatModel) statusView() string {
	separator := m.styles.status.Render("  ·  ")
	rest := m.styles.status.Render("ready")
	name := ""
	if m.session.ID == uuid.Nil {
		rest = m.styles.status.Render("choose a session to begin")
	} else {
		pct := 0
		if m.contextWindow > 0 {
			pct = min(int(m.used*100/m.contextWindow), 100)
		}
		name = m.session.Name
		thinking := m.thinking
		if thinking == "" {
			thinking = "off"
		}
		rest = strings.Join([]string{
			m.styles.status.Render(fmt.Sprintf("%s (%s)", m.session.Model, m.session.Provider)),
			m.styles.active.Render("thinking " + string(thinking)),
			m.styles.status.Render("ctx ") + contextProgressBar(m.styles, pct, 10) + m.styles.status.Render(fmt.Sprintf(" %d%%", pct)),
			m.styles.status.Render(m.session.ID.String()),
		}, separator)
	}
	if m.working {
		workStatus := m.workStatus
		if workStatus == "" {
			workStatus = "working"
		}
		rest = m.styles.statusBusy.Render(fmt.Sprintf("%s  ·  %s %s %s", m.session.Model, spinnerFrame(m.workBegan), workStatus, time.Since(m.workBegan).Round(time.Second)))
	}
	if !m.viewport.AtBottom() {
		rest += separator + m.styles.status.Render("viewing earlier output")
	}
	if name != "" {
		rest = m.styles.sessionName.Render(name) + separator + rest
	}
	if checklist := m.taskChecklistView(max(m.width-2, 1)); checklist != "" {
		rest = checklist + "\n" + rest
	}
	return fullWidth(lipgloss.NewStyle().PaddingTop(1).PaddingLeft(1).PaddingRight(1), m.width, rest)
}

func contextProgressBar(styles harnessStyles, percent, width int) string {
	filled := percent * width / 100
	if percent > 0 && filled == 0 {
		filled = 1
	}
	filled = min(max(filled, 0), width)
	empty := width - filled
	return styles.active.Render("["+strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(everforest.SurfaceAlt).Render(strings.Repeat("░", empty)+"]")
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
	lines := []string{m.styles.system.Bold(true).Render(fmt.Sprintf("TASKS  %d/%d complete", completed, len(m.checklist.Items)))}
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

func (m *chatModel) refreshTranscript(follow bool) {
	if m.viewport.Width() <= 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderTranscript())
	if follow || wasAtBottom {
		m.viewport.GotoBottom()
	}
}

func (m *chatModel) renderTranscript() string {
	width := max(m.viewport.Width()-m.viewport.Style.GetHorizontalFrameSize(), 1)
	var rendered []string
	for i := range m.blocks {
		entry := &m.blocks[i]
		streaming := m.streaming && m.streamingAt == i
		content := entry.rendered
		if streaming || !entry.renderedValid || entry.renderedWidth != width {
			content = m.renderBlock(entry, width, streaming)
			if !streaming {
				entry.rendered = content
				entry.renderedWidth = width
				entry.renderedValid = true
			}
		}
		if strings.TrimSpace(content) != "" {
			rendered = append(rendered, strings.Trim(content, "\n"))
		}
	}
	if len(rendered) == 0 {
		return m.styles.muted.Render("\nStart with a question, a task, or / for commands.")
	}
	return strings.Join(rendered, "\n\n")
}

func (m *chatModel) renderBlock(entry *block, width int, streaming bool) string {
	var content string
	switch entry.role {
	case "user":
		content = renderChatMarker("●", m.styles.userMarker, m.styles.assistant, entry.text, width)
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
		content = m.styles.thinking.Width(width).Render("CONTEXT COMPACTED\n" + entry.text)
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

func spinnerFrame(start time.Time) string {
	frames := [...]string{"◐", "◓", "◑", "◒"}
	return frames[time.Since(start).Milliseconds()/200%int64(len(frames))]
}

func animationTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return animationTickMsg{} })
}

func cancelArmTimeoutCmd(id uint64) tea.Cmd {
	return tea.Tick(cancelArmTimeout, func(time.Time) tea.Msg { return cancelArmTimeoutMsg{id: id} })
}

func (m *chatModel) startStreamRefresh() tea.Cmd {
	m.streamTick = true
	m.streamTickID++
	return streamRefreshTickCmd(m.streamTickID)
}

func (m *chatModel) stopStreamRefresh() {
	m.streamTick = false
	m.streamDirty = false
	m.streamTickID++
}

func streamRefreshTickCmd(id uint64) tea.Cmd {
	return tea.Tick(streamRefreshInterval, func(time.Time) tea.Msg { return streamRefreshTickMsg{id: id} })
}

func (m *chatModel) showSessions(sessions []contracts.SessionSummary, required bool) {
	options := []modalOption{{label: "Start a new session", detail: "choose a provider and model", status: "NEW", shortcut: 'n'}}
	for i, session := range sessions {
		status := "INACTIVE"
		if session.Active || session.ID == m.session.ID {
			status = "ACTIVE"
		}
		name := session.Name
		if name == "" {
			name = "Untitled session"
		}
		option := modalOption{
			label:   name,
			detail:  fmt.Sprintf("%s  ·  %s  ·  %s", orModel(session.Model), humanTime(session.UpdatedAt), session.Cwd),
			status:  status,
			session: session.ID,
		}
		if i < 9 {
			option.shortcut = rune('1' + i)
		}
		options = append(options, option)
	}
	m.modal = &modalModel{kind: modalSessions, title: "Sessions", body: "Start fresh or resume a saved session. The attached session is marked active.", options: options, required: required}
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

func (m *chatModel) showApproval(approval *Approval) {
	title, body := "Approval required", approval.Message
	script := ""
	var diffRows []diffRow
	if approval.ToolCall != nil {
		title, body, script = formatApprovalToolCall(*approval.ToolCall)
		if approval.Message != "" {
			body = approval.Message + "\n\n" + body
		}
		diffRows = approvalDiffRows(*approval.ToolCall)
	}
	m.modal = &modalModel{
		kind: modalApproval, title: title, body: body, script: script, diffRows: diffRows, required: true, approval: approval,
		options: []modalOption{
			{label: "Allow", shortcut: 'a', decision: "approve"},
			{label: "Deny", shortcut: 'd', decision: "deny"},
		},
	}
}

// approvalDiffRows returns structured diff rows for edit/write tool calls so the
// review pane can render the same GitHub-style diff as the transcript.
func approvalDiffRows(call contracts.ToolCall) []diffRow {
	switch contracts.ToolType(call.Function.Name) {
	case contracts.EditTool:
		if path, oldText, newText, _, ok := decodeEditArgs(call.Function.Arguments); ok {
			return editDiffRows(path, oldText, newText)
		}
	case contracts.WriteTool:
		if path, content, ok := decodeWriteArgs(call.Function.Arguments); ok {
			return writeDiffRows(path, content)
		}
	}
	return nil
}

func formatApprovalToolCall(call contracts.ToolCall) (string, string, string) {
	switch contracts.ToolType(call.Function.Name) {
	case contracts.ReadTool:
		preview, ok := readToolCallPreview(call.Function.Arguments)
		if !ok {
			return "Read file", prettyToolArguments(call.Function.Arguments), ""
		}
		return "Read file", "SOURCE  " + preview, ""

	case contracts.BashTool:
		var args struct {
			Command string `json:"command"`
			Workdir string `json:"workdir"`
			Timeout *int   `json:"timeout"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "Bash tool call", prettyToolArguments(call.Function.Arguments), ""
		}
		workdir := args.Workdir
		if workdir == "" {
			workdir = "workspace root"
		}
		metadata := []string{"The following bash script will be executed.", "", "WORKING DIRECTORY  " + workdir}
		if args.Timeout != nil {
			metadata = append(metadata, fmt.Sprintf("TIMEOUT            %d seconds", *args.Timeout))
		}
		metadata = append(metadata, "", "SCRIPT")
		return "Bash tool call", strings.Join(metadata, "\n"), args.Command

	case contracts.WriteTool:
		path, content, ok := decodeWriteArgs(call.Function.Arguments)
		if !ok {
			return "Write file", prettyToolArguments(call.Function.Arguments), ""
		}
		lineCount := 0
		if content != "" {
			lineCount = strings.Count(content, "\n") + 1
		}
		body := fmt.Sprintf("TARGET  %s\nEFFECT  Replace complete file contents\nSIZE    %d lines, %d bytes",
			path, lineCount, len([]byte(content)))
		return "Write file", body, ""

	case contracts.EditTool:
		path, _, _, replaceAll, ok := decodeEditArgs(call.Function.Arguments)
		if !ok {
			return "Edit file", prettyToolArguments(call.Function.Arguments), ""
		}
		mode := "Replace first exact match"
		if replaceAll {
			mode = "Replace every exact match"
		}
		return "Edit file", fmt.Sprintf("TARGET  %s\nMODE    %s", path, mode), ""
	}

	return strings.ReplaceAll(call.Function.Name, "_", " ") + " tool call", prettyToolArguments(call.Function.Arguments), ""
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

func (m *chatModel) handleApprovalUpdate(update ApprovalUpdate) {
	if update.Approval == nil {
		return
	}
	if update.Type == "approval_requested" {
		m.showApproval(update.Approval)
		return
	}
	if m.modal != nil && m.modal.kind == modalApproval {
		m.modal = nil
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

func (m *chatModel) appendDelta(kind contracts.DeltaKind, text string) {
	role := "assistant"
	switch kind {
	case contracts.DeltaReasoning:
		role = "thinking"
	case contracts.DeltaStatus:
		m.streaming = false
		role, text = "status", statusLabel(text)
	case contracts.DeltaCompactionSummary:
		m.streaming = false
		role = "compaction"
	case contracts.DeltaToolCall:
		m.streaming = false
		m.appendToolEvent("call", text)
		return
	case contracts.DeltaToolResult:
		m.streaming = false
		m.appendToolEvent("result", text)
		return
	case contracts.DeltaSkillLoad:
		m.streaming = false
		m.blocks = append(m.blocks, block{role: "skill", text: text})
		return
	case contracts.DeltaTaskChecklist:
		// Checklist state is rendered in the header, never as transcript text.
		return
	}
	streaming := kind == contracts.DeltaContent || kind == contracts.DeltaReasoning
	if streaming && m.streaming && m.streamingAt == len(m.blocks)-1 && m.blocks[m.streamingAt].role == role {
		m.blocks[len(m.blocks)-1].text += text
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
		m.streaming = true
		m.streamingAt = len(m.blocks) - 1
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

func resolveApprovalCmd(ctx context.Context, client Client, approval *Approval, decision string) tea.Cmd {
	return func() tea.Msg {
		if approval == nil {
			return approvalResolvedMsg{err: errors.New("approval is unavailable")}
		}
		reason := ""
		if decision == "deny" {
			reason = "denied by user"
		}
		return approvalResolvedMsg{err: client.ResolveApproval(ctx, *approval, decision, reason)}
	}
}

func blocksFromRecords(records []store.Record) []block {
	var blocks []block
	skillCallIDs := make(map[string]struct{})
	toolCallNames := make(map[string]string)
	for _, record := range records {
		if record.ToolCall != nil {
			toolCallNames[record.ToolCall.ID] = record.ToolCall.Name
		}
		if record.Message == nil || record.Message.Role != "assistant" {
			continue
		}
		for _, call := range record.Message.ToolCalls {
			toolCallNames[call.ID] = call.Function.Name
			if isSkillTool(call.Function.Name) {
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
		case store.KindToolCall:
			if record.ToolCall != nil && !isChecklistTool(record.ToolCall.Name) {
				blocks = append(blocks, block{role: "tool", text: toolCallRecordDisplay(record.ToolCall), toolKind: "call", toolName: record.ToolCall.Name, toolArgs: record.ToolCall.Arguments})
			}
		case store.KindToolResult:
			if record.ToolResult != nil {
				toolName := toolCallNames[record.ToolResult.CallID]
				if _, skill := skillCallIDs[record.ToolResult.CallID]; !skill && !isChecklistTool(toolName) {
					blocks = append(blocks, block{role: "tool", text: transcriptToolResultDisplay(toolName, record.ToolResult.Status, record.ToolResult.Output, record.ToolResult.Error), toolKind: "result", toolStatus: record.ToolResult.Status, toolName: toolName})
				}
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
					if isSkillTool(call.Function.Name) {
						blocks = append(blocks, block{role: "skill", text: skillNameFromCall(call)})
						continue
					}
					if isChecklistTool(call.Function.Name) {
						continue
					}
					blocks = append(blocks, block{role: "tool", text: toolCallPreview(call.Function.Name, call.Function.Arguments), toolKind: "call", toolName: call.Function.Name, toolArgs: call.Function.Arguments})
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
		if record.ToolCall != nil {
			toolNames[record.ToolCall.ID] = record.ToolCall.Name
		}
		if record.Message != nil && record.Message.Role == "assistant" {
			for _, call := range record.Message.ToolCalls {
				toolNames[call.ID] = call.Function.Name
			}
		}
	}
	for _, record := range records {
		if record.Compaction != nil && record.Compaction.TaskChecklist != nil {
			state = *record.Compaction.TaskChecklist
		}
		if record.ToolResult != nil && isChecklistTool(toolNames[record.ToolResult.CallID]) {
			if next, err := decodeTaskChecklist(record.ToolResult.Output); err == nil {
				state = next
			}
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
		return call.Function.Name
	}
	return call.Function.Name + " " + call.Function.Arguments
}

func toolCallRecordDisplay(call *store.ToolCallRecord) string {
	if call.Arguments == "" {
		return call.Name
	}
	return toolCallPreview(call.Name, call.Arguments)
}

func toolResultDisplay(result *store.ToolResultRecord) string {
	if result.Error != "" {
		return result.Status + ": " + result.Error
	}
	if result.Output != "" {
		return result.Status + "\n" + result.Output
	}
	return result.Status
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
		return toolResultDisplay(&store.ToolResultRecord{Status: status, Output: output, Error: resultErr})
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
	if event.Record.ToolCall != nil {
		call := event.Record.ToolCall
		if isChecklistTool(call.Name) {
			return []timelineDisplay{{role: "system", text: "task checklist updated"}}
		}
		if isSkillTool(call.Name) {
			var args struct {
				Name string `json:"name"`
			}
			if json.Unmarshal([]byte(call.Arguments), &args) == nil && args.Name != "" {
				return []timelineDisplay{{role: "skill", text: args.Name}}
			}
			return []timelineDisplay{{role: "skill", text: call.Name}}
		}
		return []timelineDisplay{{role: "tool_call", text: singleLine(toolCallRecordDisplay(call))}}
	}
	if event.Record.ToolResult != nil {
		if _, err := decodeTaskChecklist(event.Record.ToolResult.Output); err == nil {
			return []timelineDisplay{{role: "system", text: "task checklist updated"}}
		}
		return []timelineDisplay{{role: "tool_result", text: singleLine(toolResultDisplay(event.Record.ToolResult))}}
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
			if isSkillTool(call.Function.Name) {
				displays = append(displays, timelineDisplay{role: "skill", text: skillNameFromCall(call)})
				continue
			}
			if isChecklistTool(call.Function.Name) {
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
	if event.Record.Approval != nil && event.Record.Approval.ToolCall != nil {
		return []timelineDisplay{{role: "tool_call", text: singleLine(toolCallDisplay(*event.Record.Approval.ToolCall))}}
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
