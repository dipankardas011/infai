package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

type block struct {
	role       string
	text       string
	toolKind   string
	toolStatus string
	toolName   string
	skillName  string
}

type chatModel struct {
	ctx    context.Context
	client Client
	styles harnessStyles

	session store.SessionMeta
	used    int
	blocks  []block

	width            int
	height           int
	areas            []rowArea
	viewport         viewport.Model
	composer         textarea.Model
	modal            *modalModel
	commandMenu      bool
	commandSelection int

	working   bool
	workBegan time.Time
	stream    chan tea.Msg
	initCmd   tea.Cmd
}

type streamDeltaMsg struct {
	kind contracts.DeltaKind
	text string
}

type streamApprovalMsg struct{ update ApprovalUpdate }
type turnDoneMsg struct {
	reply *ChatReply
	err   error
}
type sessionLoadedMsg struct {
	meta    *store.SessionMeta
	records []store.Record
	err     error
}
type sessionsListedMsg struct {
	sessions []contracts.SessionSummary
	err      error
}
type providersListedMsg struct {
	providers []store.Provider
	switching bool
	err       error
}
type sessionCreatedMsg struct {
	meta *store.SessionMeta
	err  error
}
type modelSetMsg struct {
	provider string
	model    string
	err      error
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
	event TimelineEvent
	err   error
}
type approvalResolvedMsg struct{ err error }
type renamedMsg struct {
	meta *store.SessionMeta
	err  error
}
type animationTickMsg struct{}

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
		ctx:      ctx,
		client:   client,
		styles:   newHarnessStyles(),
		viewport: view,
		composer: input,
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
		m.reflow(true)
		return m, nil
	case streamDeltaMsg:
		m.appendDelta(msg.kind, msg.text)
		m.refreshTranscript(true)
		return m, waitStream(m.ctx, m.stream)
	case streamApprovalMsg:
		m.handleApprovalUpdate(msg.update)
		return m, waitStream(m.ctx, m.stream)
	case turnDoneMsg:
		m.working = false
		if msg.err != nil {
			m.appendError(msg.err)
		} else if msg.reply != nil {
			m.used = msg.reply.ContextTokens
			m.session.TurnCount++
			if msg.reply.Model != "" {
				m.session.Model = msg.reply.Model
			}
			if msg.reply.ContextWindow > 0 {
				m.session.ContextWindow = msg.reply.ContextWindow
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
		m.session = *msg.meta
		m.client.SetSession(msg.meta.ID)
		m.blocks = blocksFromRecords(msg.records)
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
		m.session = *msg.meta
		m.client.SetSession(msg.meta.ID)
		m.blocks = nil
		m.used = 0
		m.modal = nil
		m.refreshTranscript(true)
		return m, nil
	case modelSetMsg:
		if msg.err != nil {
			m.appendError(msg.err)
		} else {
			m.session.Provider, m.session.Model = msg.provider, msg.model
			m.blocks = append(m.blocks, block{role: "system", text: "Model switched to " + msg.model + " @ " + msg.provider})
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
	if m.modal != nil {
		return m, m.handleModalKey(msg)
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

func (m *chatModel) handleModalKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "up", "k":
		m.modal.move(-1)
	case "down", "j", "tab":
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
	m.composer.Reset()
	m.commandMenu = false
	m.reflow(false)
	if strings.HasPrefix(prompt, "/") {
		return m.runCommand(prompt)
	}
	if m.session.ID == uuid.Nil {
		m.appendError(errors.New("no active session"))
		m.refreshTranscript(true)
		return nil
	}

	m.blocks = append(m.blocks, block{role: "user", text: prompt})
	m.working = true
	m.workBegan = time.Now()
	m.refreshTranscript(true)
	m.stream = make(chan tea.Msg, 256)
	stream := m.stream
	emit := func(message tea.Msg) bool {
		select {
		case stream <- message:
			return true
		case <-m.ctx.Done():
			return false
		}
	}
	go func() {
		reply, err := m.client.Chat(m.ctx, prompt, func(kind contracts.DeltaKind, text string) {
			emit(streamDeltaMsg{kind: kind, text: text})
		}, func(update ApprovalUpdate) {
			emit(streamApprovalMsg{update: update})
		})
		emit(turnDoneMsg{reply: reply, err: err})
	}()
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
			"Enter sends · Shift+Enter adds a line · PageUp/PageDown scroll · Ctrl+O sessions · Ctrl+N new",
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
	composer := m.composerView()
	parts := []string{header, m.viewport.View(), status, commands, composer}
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
	m.composer.SetWidth(contentWidth(m.styles.composer, m.width))
	header := m.headerView()
	status := m.statusView()
	commands := m.commandMenuView()
	composer := m.composerView()
	m.areas = layoutRows(m.width, m.height, intrinsic(header), fill(), intrinsic(status), intrinsic(commands), intrinsic(composer))
	main := m.areas[1]
	m.viewport.SetWidth(main.width)
	m.viewport.SetHeight(main.height)
	m.refreshTranscript(follow)
}

func (m *chatModel) headerView() string {
	content := m.styles.brand.Render("INFAI") + " " + m.styles.headerMeta.Render("HARNESS")
	return fullWidth(m.styles.header, m.width, content)
}

func (m *chatModel) statusView() string {
	style := m.styles.status
	rest := "ready"
	name := ""
	if m.session.ID == uuid.Nil {
		rest = "choose a session to begin"
	} else {
		pct := 0
		if m.session.ContextWindow > 0 {
			pct = m.used * 100 / m.session.ContextWindow
		}
		name = m.session.Name
		rest = fmt.Sprintf("%s  ·  ctx %d%%  ·  %s", m.session.Model, pct, shortID(m.session.ID))
	}
	if m.working {
		style = m.styles.statusBusy
		rest = fmt.Sprintf("%s  ·  %s working %s", m.session.Model, spinnerFrame(m.workBegan), time.Since(m.workBegan).Round(time.Second))
	}
	if !m.viewport.AtBottom() {
		rest += "  ·  viewing earlier output"
	}
	if name != "" {
		rest = m.styles.sessionName.Render(name) + "  ·  " + style.Render(rest)
	} else {
		rest = style.Render(rest)
	}
	return fullWidth(lipgloss.NewStyle().Padding(0, 1), m.width, rest)
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
	for _, entry := range m.blocks {
		var content string
		switch entry.role {
		case "user":
			content = renderChatMarker("●", m.styles.userMarker, m.styles.assistant, entry.text, width)
		case "assistant":
			content = renderChatMarker("●", m.styles.active, lipgloss.NewStyle(), m.renderMarkdown(entry.text, max(width-2, 1)), width)
		case "thinking":
			content = renderChatMarker("◌", m.styles.muted, m.styles.thinking, entry.text, width)
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
			content = renderToolMarker(marker, markerStyle, m.styles.tool, entry.toolName, entry.text, width)
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
	body := strings.TrimSpace(name + " " + detail)
	indent := lipgloss.Width(marker) + 1
	lines := strings.Split(strings.Trim(lipgloss.Wrap(body, max(width-indent, 1), ""), "\n"), "\n")
	for i := range lines {
		if i == 0 {
			rest := strings.TrimPrefix(lines[i], name)
			lines[i] = markerStyle.Render(marker) + " " + markerStyle.Bold(true).Render(name) + bodyStyle.Render(rest)
			continue
		}
		lines[i] = strings.Repeat(" ", indent) + bodyStyle.Render(lines[i])
	}
	return strings.Join(lines, "\n")
}

func (m *chatModel) renderMarkdown(markdown string, width int) string {
	markdown = normalizeMarkdownMath(markdown)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(everforestMarkdownStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return m.styles.assistant.Width(width).Render(markdown)
	}
	output, err := renderer.Render(markdown)
	if err != nil {
		return m.styles.assistant.Width(width).Render(markdown)
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

func (m *chatModel) showModels(providers []store.Provider, switching bool) {
	var options []modalOption
	for _, provider := range providers {
		for _, model := range provider.ModelNames() {
			options = append(options, modalOption{label: model + "  @ " + provider.Name, provider: provider.Name, model: model})
		}
	}
	if len(options) == 0 {
		m.showNotice("No models configured", "Add a provider and model to models.json, then restart the server.", false)
		return
	}
	m.modal = &modalModel{kind: modalModels, title: "Choose a model", body: "The model is applied to this session.", options: options, switching: switching}
}

func (m *chatModel) showCommands() {
	m.commandMenu = true
	m.commandSelection = 0
}

func (m *chatModel) showApproval(approval *Approval) {
	body := approval.Message
	if body == "" && approval.ToolCall != nil {
		body = fmt.Sprintf("%s\n%s", approval.ToolCall.Function.Name, approval.ToolCall.Function.Arguments)
	}
	m.modal = &modalModel{
		kind: modalApproval, title: "Approval required", body: body, required: true, approval: approval,
		options: []modalOption{
			{label: "Allow this operation", shortcut: 'a', decision: "approve"},
			{label: "Deny this operation", shortcut: 'd', decision: "deny"},
		},
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
		role, text = "status", statusLabel(text)
	case contracts.DeltaCompactionSummary:
		role = "compaction"
	case contracts.DeltaToolCall:
		m.appendToolEvent("call", text)
		return
	case contracts.DeltaToolResult:
		m.appendToolEvent("result", text)
		return
	case contracts.DeltaSkillLoad:
		m.blocks = append(m.blocks, block{role: "skill", text: text})
		return
	}
	if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].role == role && role != "status" {
		m.blocks[len(m.blocks)-1].text += text
		return
	}
	if role == "status" && len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].role == role {
		m.blocks[len(m.blocks)-1].text = text
		return
	}
	m.blocks = append(m.blocks, block{role: role, text: text})
}

func (m *chatModel) appendToolEvent(kind, text string) {
	status := ""
	if kind == "result" {
		if strings.HasSuffix(text, "[success]") {
			status = "success"
		} else {
			status = "error"
		}
	}
	name := ""
	if fields := strings.Fields(text); len(fields) > 0 {
		name = fields[0]
	}
	m.blocks = append(m.blocks, block{role: "tool", text: text, toolKind: kind, toolStatus: status, toolName: name})
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
		return sessionLoadedMsg{meta: meta, records: records, err: err}
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
		providers, err := client.ListProviders(ctx)
		return providersListedMsg{providers: providers, switching: switching, err: err}
	}
}

func createSessionCmd(ctx context.Context, client Client, provider, model string) tea.Cmd {
	return func() tea.Msg {
		cwd, _ := os.Getwd()
		meta, err := client.CreateSession(ctx, SessionCreateOptions{Provider: provider, Model: model, Cwd: cwd})
		return sessionCreatedMsg{meta: meta, err: err}
	}
}

func setModelCmd(ctx context.Context, client Client, provider, model string) tea.Cmd {
	return func() tea.Msg {
		err := client.SetSessionModel(ctx, provider, model)
		return modelSetMsg{provider: provider, model: model, err: err}
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
		err := client.SelectBranch(ctx, sessionID, event.ID)
		return branchSelectedMsg{event: event, err: err}
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
			if record.ToolCall != nil {
				blocks = append(blocks, block{role: "tool", text: toolCallRecordDisplay(record.ToolCall), toolKind: "call", toolName: record.ToolCall.Name})
			}
		case store.KindToolResult:
			if record.ToolResult != nil {
				if _, skill := skillCallIDs[record.ToolResult.CallID]; !skill {
					blocks = append(blocks, block{role: "tool", text: toolResultDisplay(record.ToolResult), toolKind: "result", toolStatus: record.ToolResult.Status, toolName: toolCallNames[record.ToolResult.CallID]})
				}
			}
		case store.KindMessage:
			if record.Message == nil {
				continue
			}
			message := record.Message
			switch message.Role {
			case "user":
				blocks = append(blocks, block{role: "user", text: message.Text()})
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
					blocks = append(blocks, block{role: "tool", text: toolCallDisplay(call), toolKind: "call", toolName: call.Function.Name})
				}
			case "tool":
				if _, skill := skillCallIDs[message.ToolCallID]; !skill {
					blocks = append(blocks, block{role: "tool", text: message.Text(), toolKind: "result", toolStatus: "success", toolName: toolCallNames[message.ToolCallID]})
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
	return call.Name + " " + call.Arguments
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
		return []timelineDisplay{{role: "assistant", text: "content unavailable"}}
	}
	if event.Record.ToolCall != nil {
		call := event.Record.ToolCall
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
		return []timelineDisplay{{role: "tool_result", text: singleLine(toolResultDisplay(event.Record.ToolResult))}}
	}
	if event.Record.Message != nil {
		message := event.Record.Message
		if message.Role == "user" {
			return []timelineDisplay{{role: "user", text: singleLine(message.Text())}}
		}
		if message.Role == "tool" {
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
