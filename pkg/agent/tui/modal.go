package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type modalKind int

const (
	modalSessions modalKind = iota
	modalModels
	modalCommands
	modalTimeline
	modalNotice
)

type modalOption struct {
	label    string
	role     string
	current  bool
	tree     string
	fork     string
	shortcut rune
	provider string
	model    string
	session  uuid.UUID
	event    *TimelineEvent
	// detailParts is the identifying fields of a session row, most important
	// first, so a narrow list can drop the ones that fit worst.
	detailParts []string
	// agentKind is the kind of agent the session runs, which the list names
	// beside the status.
	agentKind contracts.AgentKind
	// sessionStatus is the status the engine reported for the session, which
	// the session list shows as a glyph and a label.
	sessionStatus contracts.SessionStatus
	// sessionActive is whether the engine is holding the session. It is the
	// engine's own answer, and the status cannot stand in for it: a session
	// that concluded stays in the engine until it is closed, so it reports a
	// concluded status while still being open to close.
	sessionActive bool
}

type modalModel struct {
	kind      modalKind
	title     string
	body      string
	options   []modalOption
	selected  int
	switching bool
	required  bool
	// pendingDelete is the session a first "d" armed for deletion. The second
	// "d" deletes it; anything else drops the arm.
	pendingDelete uuid.UUID
}

func (m *modalModel) move(delta int) {
	if len(m.options) == 0 {
		return
	}
	m.selected = (m.selected + delta + len(m.options)) % len(m.options)
}

func (m *modalModel) optionForShortcut(key rune) (int, bool) {
	for i, option := range m.options {
		if option.shortcut != 0 && option.shortcut == key {
			return i, true
		}
	}
	return 0, false
}

func renderModal(m *modalModel, width, height int, styles harnessStyles) string {
	if m == nil || width <= 0 || height <= 0 {
		return ""
	}
	modalStyle := styles.modal
	if modalStyle.GetHorizontalFrameSize() >= width || modalStyle.GetVerticalFrameSize() >= height {
		modalStyle = modalStyle.Padding(0)
	}
	labels := make([]string, 0, len(m.options)+2)
	labels = append(labels, m.title, m.body)
	for _, option := range m.options {
		label := option.label
		if m.kind == modalTimeline {
			label = option.tree + timelineForkLabel(option.fork) + timelineRoleLabel(option.role) + label
		}
		labels = append(labels, label)
	}
	frameWidth := modalStyle.GetHorizontalFrameSize()
	boxWidth := min(intrinsicTextWidth(labels...)+frameWidth+2, width)
	innerWidth := contentWidth(modalStyle, boxWidth)

	var rows []string
	rows = append(rows, styles.modalTitle.Render(strings.ToUpper(m.title)))
	if m.body != "" {
		body := styles.modalBody.Width(innerWidth).Render(m.body)
		reserved := lipgloss.Height(rows[0])
		if len(m.options) > 0 {
			reserved += 2 // section gap and at least one selectable row
		}
		bodyHeight := max(height-modalStyle.GetVerticalFrameSize()-reserved, 0)
		body = lipgloss.NewStyle().MaxHeight(bodyHeight).Render(body)
		if body != "" {
			rows = append(rows, "", body)
		}
	}
	if len(m.options) > 0 {
		rows = append(rows, "")
	}

	chromeHeight := lipgloss.Height(strings.Join(rows, "\n")) + modalStyle.GetVerticalFrameSize()
	start, end := visibleRange(len(m.options), m.selected, height-chromeHeight)
	showRange := start > 0 || end < len(m.options)
	if showRange && end-start > 1 {
		start, end = visibleRange(len(m.options), m.selected, end-start-1)
	}
	for i := start; i < end; i++ {
		option := m.options[i]
		if m.kind == modalTimeline && innerWidth >= 5 {
			rows = append(rows, renderTimelineOption(option, i == m.selected, innerWidth, styles))
			continue
		}
		shortcut := ""
		if option.shortcut != 0 {
			shortcut = fmt.Sprintf("  [%c]", option.shortcut)
		}
		label := option.label
		maxLabel := max(innerWidth-lipgloss.Width(shortcut)-3, 1)
		label = lipgloss.NewStyle().MaxWidth(maxLabel).Render(label)
		line := label + shortcut
		if i == m.selected {
			line = styles.modalActive.Width(innerWidth).Render("› " + line)
		} else {
			line = styles.modalOption.Width(innerWidth).Render(line)
		}
		rows = append(rows, line)
	}
	if showRange {
		rows = append(rows, styles.muted.Render(fmt.Sprintf("  %d-%d of %d", start+1, end, len(m.options))))
	}

	return modalStyle.Width(boxWidth).MaxHeight(height).Render(strings.Join(rows, "\n"))
}

func renderTimelineOption(option modalOption, selected bool, width int, styles harnessStyles) string {
	rowStyle := styles.modalOption.PaddingLeft(0)
	cursor := "  "
	if selected {
		rowStyle = styles.modalActive.PaddingLeft(0)
		cursor = "› "
	}
	markerStyle := rowStyle
	marker := "  "
	if option.current {
		markerStyle = markerStyle.Foreground(everforest.Red).Bold(true)
		marker = "* "
	}
	available := width - 4
	fork := timelineForkLabel(option.fork)
	role := timelineRoleLabel(option.role)
	chromeWidth := lipgloss.Width(option.tree) + lipgloss.Width(fork) + lipgloss.Width(role)
	if chromeWidth >= available {
		label := ansi.Truncate(option.tree+fork+role+option.label, available, "…")
		return lipgloss.JoinHorizontal(lipgloss.Top,
			rowStyle.Width(2).Render(cursor),
			markerStyle.Width(2).Render(marker),
			rowStyle.Width(available).Render(label),
		)
	}
	labelWidth := available - chromeWidth
	label := lipgloss.NewStyle().MaxWidth(labelWidth).Render(option.label)
	forkStyle := rowStyle
	if option.fork == "branch" {
		forkStyle = forkStyle.Foreground(everforest.Purple).Bold(true)
	}
	roleStyle := timelineRoleStyle(rowStyle, option.role)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		rowStyle.Width(2).Render(cursor),
		markerStyle.Width(2).Render(marker),
		rowStyle.Render(option.tree),
		forkStyle.Render(fork),
		roleStyle.Render(role),
		rowStyle.Width(labelWidth).Render(label),
	)
}

func timelineForkLabel(fork string) string {
	if fork == "branch" {
		return "⎇  "
	}
	return ""
}

func timelineRoleLabel(role string) string {
	if role == "" {
		return ""
	}
	return role + ": "
}

// timelineRoleStyle resolves a timeline role to the theme's semantic colour.
// The roles are the ones timelineEventDisplays emits, and they match the
// timelineRoleColor palette in colors.go so the modal timeline and the
// branch-selection screen read the same.
func timelineRoleStyle(base lipgloss.Style, role string) lipgloss.Style {
	switch role {
	case "user":
		return base.Foreground(everforest.Blue)
	case "assistant":
		return base.Foreground(everforest.Green)
	case "thinking", "tool_result":
		return base.Foreground(everforest.Muted)
	case "system":
		return base.Foreground(everforest.Purple)
	case "tool_call":
		return base.Foreground(everforest.Text)
	case "skill":
		return base.Foreground(everforest.Aqua)
	default:
		return base.Foreground(everforest.Text)
	}
}

func renderSelectionScreen(m *modalModel, width, height int, styles harnessStyles) string {
	if m == nil || width <= 0 || height <= 0 {
		return ""
	}
	if m.kind == modalSessions {
		return renderSessionWorkspace(m, width, height, styles)
	}
	contentWidth := max(width-4, 1)
	header := styles.screenTitle.Render(strings.ToUpper(m.title))
	if m.body != "" {
		header += "\n" + styles.screenBody.Width(contentWidth).Render(m.body)
	}
	header += "\n"
	footer := styles.inactive.Render("↑/↓ navigate  ·  enter select")
	if !m.required {
		footer += styles.inactive.Render("  ·  esc back")
	}
	capacity := max(height-lipgloss.Height(header)-lipgloss.Height(footer)-2, 1)
	start, end := visibleRange(len(m.options), m.selected, capacity)

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		option := m.options[i]
		rowStyle := styles.screenRow
		prefix := "  "
		if i == m.selected {
			rowStyle = styles.screenSel
			prefix = "› "
		}
		line := ansi.Truncate(prefix+option.label, contentWidth, "…")
		rows = append(rows, rowStyle.Width(contentWidth).Render(line))
	}
	if start > 0 || end < len(m.options) {
		footer = styles.inactive.Render(fmt.Sprintf("%d-%d of %d  ·  ", start+1, end, len(m.options))) + footer
	}

	main := strings.Join(rows, "\n")
	tracks := layoutRows(width, height,
		intrinsic(fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header)),
		fill(),
		intrinsic(fullWidth(lipgloss.NewStyle().Padding(0, 2), width, footer)),
	)
	parts := []string{
		fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header),
		fullWidth(lipgloss.NewStyle().Padding(0, 2), width, main),
		fullWidth(lipgloss.NewStyle().Padding(0, 2), width, footer),
	}
	for i := range parts {
		parts[i] = fitArea(tracks[i], parts[i])
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func renderSessionWorkspace(m *modalModel, width, height int, styles harnessStyles) string {
	header := fullWidth(lipgloss.NewStyle().Padding(1, 2), width,
		styles.brand.Render("INFAI")+" "+styles.headerMeta.Render("HARNESS")+"\n"+
			styles.screenTitle.Render("SESSION WORKSPACE")+"\n"+
			styles.screenBody.Render("Start fresh, inspect active work, or resume a saved session."))
	footerText := "n new  ·  ↑/↓ navigate sessions  ·  enter open  ·  c close  ·  d delete"
	if m.pendingDelete != uuid.Nil {
		footerText = "press d again to delete this session  ·  any other key cancels"
	}
	if !m.required {
		footerText += "  ·  esc back"
	}
	footer := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, styles.inactive.Render(footerText))

	contentHeight := max(height-lipgloss.Height(header)-lipgloss.Height(footer), 0)
	innerWidth := max(width-4, 1)
	newPanel := renderNewSessionPanel(m, innerWidth, styles)
	listHeight := max(contentHeight-lipgloss.Height(newPanel)-1, 6)
	main := newPanel + "\n" + renderSessionsPanel(m, innerWidth, listHeight, styles)
	if pad := contentHeight - lipgloss.Height(main); pad > 0 {
		main += strings.Repeat("\n", pad)
	}
	body := lipgloss.NewStyle().Padding(0, 2).Render(main)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// sessionStatusDescriptor is a reported status in the terms the UI shows it: a
// glyph that survives a narrow row, and the label that spells it out.
type sessionStatusDescriptor struct {
	glyph string
	label string
	style lipgloss.Style
}

// describeSessionStatus maps a session status to its presentation. The status
// row and the session list both read it, so a status never means one thing on
// the chat screen and another in the list.
func describeSessionStatus(status contracts.SessionStatus, styles harnessStyles) sessionStatusDescriptor {
	switch status {
	case contracts.SessionBusy:
		return sessionStatusDescriptor{"◐", "busy", styles.statusBusy}
	case contracts.SessionWaitingApproval:
		return sessionStatusDescriptor{"⚑", "waiting for approval", styles.statusWaiting}
	case contracts.SessionCompacting:
		return sessionStatusDescriptor{"⟳", "compacting", styles.statusBusy}
	case contracts.SessionCompleted:
		return sessionStatusDescriptor{"✓", "completed", styles.statusConcluded}
	case contracts.SessionMaxIterationExhausted:
		return sessionStatusDescriptor{"⚠", "max iterations reached", styles.error}
	case contracts.SessionTombstone:
		return sessionStatusDescriptor{"·", "inactive", styles.status}
	default:
		return sessionStatusDescriptor{"○", "idle", styles.statusOpen}
	}
}

func renderNewSessionPanel(m *modalModel, width int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	label := "Start a new session"
	if len(m.options) > 0 && m.options[0].shortcut != 0 {
		label = fmt.Sprintf("[%c] for a new session", m.options[0].shortcut)
	}
	rowStyle, border, prefix := styles.screenRow, everforest.SurfaceAlt, "  "
	if m.selected == 0 {
		rowStyle, border, prefix = styles.screenSel, everforest.Green, "› "
	}
	return renderSessionPanel("NEW SESSION", sessionRow(rowStyle, prefix+label, contentWidth), width, 0, border, styles)
}

func renderSessionsPanel(m *modalModel, width, height int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	// The first option is the new-session row, which has a panel of its own.
	options := m.options
	if len(options) > 0 {
		options = options[1:]
	}
	if len(options) == 0 {
		return renderSessionPanel("SESSIONS", styles.inactive.Render("No saved sessions yet"), width, height, everforest.SurfaceAlt, styles)
	}
	selected := clamp(m.selected-1, 0, len(options)-1)
	rowCap := max((height-5)/2, 1) // each session takes a name row and a detail row
	start, end := visibleRange(len(options), selected, rowCap)
	rows := make([]string, 0, (end-start)*2+1)
	for i := start; i < end; i++ {
		option := options[i]
		armed := option.session != uuid.Nil && option.session == m.pendingDelete
		rows = append(rows, sessionEntryRows(option, m.selected == i+1, armed, contentWidth, styles)...)
	}
	if start > 0 || end < len(options) {
		rows = append(rows, styles.inactive.Render(fmt.Sprintf("  %d-%d of %d", start+1, end, len(options))))
	}
	border := everforest.SurfaceAlt
	if m.selected > 0 {
		border = everforest.Green
	}
	return renderSessionPanel("SESSIONS", strings.Join(rows, "\n"), width, height, border, styles)
}

func sessionEntryRows(option modalOption, selected, armed bool, width int, styles harnessStyles) []string {
	status := describeSessionStatus(option.sessionStatus, styles)
	if armed {
		// The armed row says so where its status was, so the decision and the
		// row it applies to are read together.
		status = sessionStatusDescriptor{"!", "delete?", styles.error}
	}
	prefix := "  "
	if selected {
		prefix = "› "
	}

	kindGlyph, kindWord, kindStyle := "", "", lipgloss.NewStyle()
	switch option.agentKind {
	case contracts.InteractiveAgent:
		kindGlyph, kindWord, kindStyle = "⬢", "interactive", styles.agentInteractive
	case contracts.SidecarLoopAgent:
		kindGlyph, kindWord, kindStyle = "⧉", "sidecar_loop", styles.agentSidecar
	case contracts.SingleLoopAgent:
		kindGlyph, kindWord, kindStyle = "↻", "loop", styles.agentLoop
	case contracts.SwarmAgent:
		kindGlyph, kindWord, kindStyle = "⇶", "swarm", styles.agentSwarm
	}

	type rowTail struct{ plain, styled string }
	kindGroup := strings.TrimSpace(kindGlyph + " " + kindWord)
	statusGroup := status.glyph + " " + status.label
	rich := rowTail{
		plain:  kindGroup + "  " + statusGroup,
		styled: kindStyle.Render(kindGroup) + "  " + status.style.Render(statusGroup),
	}
	marks := rowTail{
		plain:  status.glyph,
		styled: status.style.Render(status.glyph),
	}
	if kindGlyph == "" {
		// A session with no kind recorded has only the status to show.
		rich = rowTail{plain: statusGroup, styled: status.style.Render(statusGroup)}
	} else {
		marks = rowTail{
			plain:  kindGlyph + "  " + status.glyph,
			styled: kindStyle.Render(kindGlyph) + "  " + status.style.Render(status.glyph),
		}
	}

	nameWidth := func(tail string) int {
		return min(width-lipgloss.Width(prefix)-lipgloss.Width(tail)-2, maxSessionNameWidth)
	}

	wanted := min(lipgloss.Width(option.label), maxSessionNameWidth)
	tail := marks
	if wanted <= nameWidth(rich.plain) {
		tail = rich
	}
	name := ansi.Truncate(option.label, max(nameWidth(tail.plain), 1), "…")
	gap := strings.Repeat(" ", max(width-lipgloss.Width(prefix)-lipgloss.Width(name)-lipgloss.Width(tail.plain), 1))
	detail := "    " + sessionDetailLine(option.detailParts, max(width-4, 1))

	if selected {
		return []string{
			sessionRow(styles.screenSel, prefix+name+gap+tail.plain, width),
			sessionRow(styles.inactive.Background(everforest.SelectionBg), detail, width),
		}
	}
	return []string{
		sessionRow(styles.screenRow, prefix+name+gap+tail.styled, width),
		sessionRow(styles.inactive, detail, width),
	}
}

// maxSessionNameWidth caps the name so a long one cannot squeeze the marks off
// the row: the marks are what the list is scanned for.
const maxSessionNameWidth = 60

// sessionDetailLine joins a session's identifying fields, dropping the ones that
// fit worst so the line still fits a narrow terminal.
func sessionDetailLine(parts []string, width int) string {
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, sessionFieldSeparator)) > width {
		parts = parts[:len(parts)-1]
	}
	return ansi.Truncate(strings.Join(parts, sessionFieldSeparator), width, "…")
}

const sessionFieldSeparator = "  ·  "

func renderSessionPanel(title, body string, width, height int, border color.Color, styles harnessStyles) string {
	content := styles.screenTitle.Render(title)
	if body != "" {
		content += "\n\n" + body
	}
	if height > 0 {
		if pad := height - 2 - lipgloss.Height(content); pad > 0 {
			content += strings.Repeat("\n", pad)
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(width).
		Render(content)
}

// sessionRow renders a single line at exactly contentWidth cells so the panel
// border stays flush and selected rows get a full-width highlight.
func sessionRow(style lipgloss.Style, text string, contentWidth int) string {
	return style.Width(contentWidth).Render(ansi.Truncate(text, contentWidth, "…"))
}
