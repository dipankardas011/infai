package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textarea"
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
	modalPromptArgs
)

type modalOption struct {
	label   string
	role    string
	current bool
	tree    string
	// detailTree continues ancestor branches through the session's detail row.
	detailTree string
	fork       string
	shortcut   rune
	provider   string
	model      string
	session    uuid.UUID
	event      *TimelineEvent
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

// searchHaystack is what a branch-timeline search matches: the role and the
// row's own display text, without the tree that positions it.
func (o modalOption) searchHaystack() string {
	if o.role == "" {
		return o.label
	}
	return o.role + ": " + o.label
}

// promptField is one argument row of the prompt-argument form.
type promptField struct {
	name     string
	help     string
	required bool
	// input holds the argument's value. The form edits each argument in the
	// same textarea the composer uses, so a value is typed the way text is
	// typed everywhere else in the harness.
	input textarea.Model
}

type modalModel struct {
	kind      modalKind
	title     string
	body      string
	options   []modalOption
	selected  int
	switching bool
	required  bool
	// promptServer, promptName, promptFields and promptFocus are the
	// prompt-argument form: the prompt it will render, one row per declared
	// argument, and the row that owns the keyboard.
	promptServer string
	promptName   string
	promptFields []promptField
	promptFocus  int
	// pendingDelete is the session a first "d" armed for deletion. The second
	// "d" deletes it; anything else drops the arm.
	pendingDelete uuid.UUID
	// query, searching, matches and matchIndex are the branch timeline's
	// search: the text typed so far, the rows it hit, and where the cursor sits
	// among them. A hit moves the cursor; the list is never filtered, so the
	// branch structure keeps its shape while searching.
	query      string
	searching  bool
	matches    []int
	matchIndex int
}

// search runs query over the timeline's rows and puts the cursor on the first
// hit. A row is matched whole — role and label, not the tree that positions it.
func (m *modalModel) search(query string) {
	m.query = query
	m.matches = m.matches[:0]
	if needle := strings.ToLower(strings.TrimSpace(query)); needle != "" {
		for i, option := range m.options {
			if strings.Contains(strings.ToLower(option.searchHaystack()), needle) {
				m.matches = append(m.matches, i)
			}
		}
	}
	if len(m.matches) > 0 {
		m.matchIndex = 0
		m.selected = m.matches[0]
	}
}

// nextMatch moves the cursor to the next hit, or the previous one for a
// negative delta, wrapping at each end.
func (m *modalModel) nextMatch(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.matchIndex = (m.matchIndex + delta + len(m.matches)) % len(m.matches)
	m.selected = m.matches[m.matchIndex]
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

// timelineSearchLine is the branch timeline's search prompt: the query, a caret
// while it is being typed, and how many rows it hit and which one the cursor is
// on.
func (m *modalModel) timelineSearchLine(styles harnessStyles) string {
	line := styles.screenBody.Render("/") + styles.screenRow.Render(m.query)
	if m.searching {
		line += styles.active.Render("▏")
	}
	switch {
	case m.query == "":
	case len(m.matches) == 0:
		line += "  " + styles.error.Render("no matches")
	default:
		line += "  " + styles.inactive.Render(fmt.Sprintf("%d/%d matches", m.matchIndex+1, len(m.matches)))
	}
	return line
}

// renderTimelineRow renders one branch-timeline row at exactly width cells: a
// two-cell cursor, a two-cell current-event marker, then the tree, the fork
// glyph, the role, and the label, which takes what is left.
func renderTimelineRow(option modalOption, selected bool, width int, styles harnessStyles) string {
	rowStyle := styles.screenRow
	cursor := "  "
	if selected {
		rowStyle = styles.screenSel
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
	// Truncate rather than let the frame wrap: one display is one line, so the
	// rows the screen counts are the lines it draws.
	label := rowStyle.Width(labelWidth).Render(ansi.Truncate(option.label, labelWidth, "…"))
	forkStyle := rowStyle
	if option.fork == "branch" {
		forkStyle = forkStyle.Foreground(everforest.Purple).Bold(true)
	}
	roleStyle := timelineRoleStyle(rowStyle, option.role)
	// The tree is structure, not content: it recedes to the faintest colour so
	// the guides do not compete with the rows they connect.
	treeStyle := rowStyle.Foreground(everforest.Faint)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		rowStyle.Width(2).Render(cursor),
		markerStyle.Width(2).Render(marker),
		treeStyle.Render(option.tree),
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
	case "thinking":
		return base.Foreground(everforest.Muted)
	case "tool_result":
		return base.Foreground(everforest.Orange)
	case "event":
		return base.Foreground(everforest.Orange)
	case "system":
		return base.Foreground(everforest.Purple)
	case "tool_call":
		return base.Foreground(everforest.Purple)
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
	if m.kind == modalPromptArgs {
		return renderPromptArgsPanel(m, width, height, styles)
	}
	contentWidth := max(width-4, 1)
	header := styles.heading(m.title, everforest.Background)
	if m.body != "" {
		header += "\n" + styles.screenBody.Width(contentWidth).Render(m.body)
	}
	if m.kind == modalTimeline && (m.searching || m.query != "") {
		header += "\n" + m.timelineSearchLine(styles)
	}
	header += "\n"
	var footer string
	switch {
	case m.kind == modalTimeline && m.searching:
		footer = styles.inactive.Render("type to search  ·  enter done  ·  esc cancel")
	case m.kind == modalTimeline:
		footer = styles.inactive.Render("↑/↓ move  ·  / search  ·  n/N hits  ·  enter branch  ·  esc back")
	default:
		footer = styles.inactive.Render("↑/↓ navigate  ·  enter select")
		if !m.required {
			footer += styles.inactive.Render("  ·  esc back")
		}
	}
	capacity := max(height-lipgloss.Height(header)-lipgloss.Height(footer)-2, 1)
	start, end := visibleRange(len(m.options), m.selected, capacity)

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		option := m.options[i]
		if m.kind == modalTimeline {
			rows = append(rows, renderTimelineRow(option, i == m.selected, contentWidth, styles))
			continue
		}
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
		styles.heading("Session workspace", everforest.Background)+"\n"+
			styles.screenBody.Render("Start fresh, inspect active work, or resume a saved session."))
	footerText := "n new  ·  ↑/↓ navigate sessions  ·  enter open  ·  c close  ·  d delete"
	if m.pendingDelete != uuid.Nil {
		footerText = "press d again to permanently delete this session and its sidecars  ·  active work stops  ·  any other key cancels"
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

// agentKindMark is the glyph and the colour an agent kind wears wherever the UI
// names it: the session list and the session row both read it, so a kind never
// looks like two different things on two screens.
func agentKindMark(kind contracts.AgentKind, styles harnessStyles) (string, lipgloss.Style) {
	switch kind {
	case contracts.InteractiveAgent:
		return "⬢", styles.agentInteractive
	case contracts.SidecarLoopAgent:
		return "⧉", styles.agentSidecar
	case contracts.SingleLoopAgent:
		return "↻", styles.agentLoop
	case contracts.SwarmAgent:
		return "⇶", styles.agentSwarm
	default:
		return "", lipgloss.NewStyle()
	}
}

// agentKindLabel is the name an agent kind spells itself with in the list, once
// the row has room for it.
func agentKindLabel(kind contracts.AgentKind) string {
	switch kind {
	case contracts.InteractiveAgent:
		return "interactive"
	case contracts.SidecarLoopAgent:
		return "sidecar_loop"
	case contracts.SingleLoopAgent:
		return "loop"
	case contracts.SwarmAgent:
		return "swarm"
	default:
		return ""
	}
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

// renderPromptArgsPanel draws an MCP prompt's argument form: one labelled
// textarea per declared argument, the focused one framed in the accent colour,
// and a footer that names what is still missing.
func renderPromptArgsPanel(m *modalModel, width, height int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	innerWidth := max(contentWidth-4, 1) // the box's border and padding
	m.layoutPromptArgs(width)

	header := styles.heading(m.title, everforest.Background)
	if m.body != "" {
		header += "\n" + styles.screenBody.Width(contentWidth).Render(m.body)
	}
	header += "\n"

	missing := make([]string, 0, len(m.promptFields))
	for _, field := range m.promptFields {
		if field.required && strings.TrimSpace(field.input.Value()) == "" {
			missing = append(missing, field.name)
		}
	}

	// Each argument costs a label line and a three-line box, so the window is
	// measured in whole arguments and follows the focused one.
	const linesPerField = 4
	capacity := max(max(height-6, 1)/linesPerField, 1)
	start, end := visibleRange(len(m.promptFields), m.promptFocus, capacity)

	rows := make([]string, 0, (end-start)*linesPerField)
	for i := start; i < end; i++ {
		field := m.promptFields[i]
		label := "  " + field.name
		if field.required {
			label += " *"
		}
		labelStyle := styles.screenRow
		if i == m.promptFocus {
			label = "› " + field.name
			if field.required {
				label += " *"
			}
			labelStyle = styles.screenSel
		}
		if field.required && strings.TrimSpace(field.input.Value()) == "" {
			// A required argument with no value blocks the render, so its
			// label warns whether or not the field holds the cursor.
			labelStyle = labelStyle.Foreground(everforest.Red).Bold(true)
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(innerWidth)
		if i == m.promptFocus {
			box = box.BorderForeground(everforest.Green)
		} else {
			box = box.BorderForeground(everforest.SurfaceAlt)
		}
		rows = append(rows, labelStyle.Render(ansi.Truncate(label, contentWidth, "…")), box.Render(field.input.View()))
	}
	if start > 0 || end < len(m.promptFields) {
		rows = append(rows, styles.inactive.Render(fmt.Sprintf("  %d-%d of %d", start+1, end, len(m.promptFields))))
	}

	footer := "tab next  ·  enter render  ·  esc cancel"
	footerStyle := styles.inactive
	if len(missing) > 0 {
		// Enter does nothing until the required values are in, so the footer
		// that asks for them reads as blocking.
		footer = "fill " + strings.Join(missing, ", ") + "  ·  tab next  ·  enter render  ·  esc cancel"
		footerStyle = styles.error
	}
	footer = ansi.Truncate(footer, contentWidth, "…")

	tracks := layoutRows(width, height,
		intrinsic(fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header)),
		fill(),
		intrinsic(fullWidth(lipgloss.NewStyle().Padding(0, 2), width, footerStyle.Render(footer))),
	)
	parts := []string{
		fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header),
		fullWidth(lipgloss.NewStyle().Padding(0, 2), width, strings.Join(rows, "\n")),
		fullWidth(lipgloss.NewStyle().Padding(0, 2), width, footerStyle.Render(footer)),
	}
	for i := range parts {
		parts[i] = fitArea(tracks[i], parts[i])
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// layoutPromptArgs sizes every argument input to the screen and hands the
// keyboard to the focused one, so a resize while the form is open keeps the
// boxes and the caret where the screen says they are.
func (m *modalModel) layoutPromptArgs(width int) {
	inner := max(width-8, 8)
	for i := range m.promptFields {
		field := &m.promptFields[i]
		field.input.SetWidth(inner)
		if i == m.promptFocus {
			_ = field.input.Focus()
			continue
		}
		field.input.Blur()
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
	rowStyle := styles.screenRow
	prefix := "  "
	if selected {
		rowStyle = styles.screenSel
		prefix = "› "
	}

	kindGlyph, kindStyle := agentKindMark(option.agentKind, styles)
	kindWord := agentKindLabel(option.agentKind)

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
		return min(width-lipgloss.Width(prefix)-lipgloss.Width(option.tree)-lipgloss.Width(tail)-2, maxSessionNameWidth)
	}

	wanted := min(lipgloss.Width(option.label), maxSessionNameWidth)
	tail := marks
	if wanted <= nameWidth(rich.plain) {
		tail = rich
	}
	label := ansi.Truncate(option.label, max(nameWidth(tail.plain), 1), "…")
	gap := strings.Repeat(" ", max(width-lipgloss.Width(prefix)-lipgloss.Width(option.tree)-lipgloss.Width(label)-lipgloss.Width(tail.plain), 1))
	tailWidth := max(width-lipgloss.Width(prefix)-lipgloss.Width(option.tree)-lipgloss.Width(label)-lipgloss.Width(gap), 1)
	tailSegment := lipgloss.NewStyle().Width(tailWidth).Render(ansi.Truncate(tail.styled, tailWidth, "…"))
	if selected {
		// The chosen row keeps one colour for its text and its trailing run, so
		// the highlight stays unbroken; only the tree steps out of it.
		tailSegment = rowStyle.Width(tailWidth).Render(ansi.Truncate(tail.plain, tailWidth, "…"))
	}
	// The tree is structure, not content: it recedes to the faintest colour,
	// and the row style is re-opened after it so the reset that ends it does not
	// take the rest of the row with it.
	line := rowStyle.Render(prefix) + sessionTree(option.tree, rowStyle) + rowStyle.Render(label+gap) + tailSegment
	detailStyle := styles.inactive
	if selected {
		detailStyle = detailStyle.Background(everforest.SelectionBg)
	}
	detailText := sessionDetailLine(option.detailParts, max(width-4, 1))
	detail := detailStyle.Render("    " + detailText)
	if option.tree != "" {
		detail = detailStyle.Render("  ") +
			sessionTree(option.detailTree, detailStyle) +
			detailStyle.Render(detailText)
	}
	return []string{sessionRow(rowStyle, line, width), sessionRow(detailStyle, detail, width)}
}

// sessionTree renders a session row's hierarchy prefix. The tree is structure,
// not content, so it recedes to the faintest colour, the same way the branch
// timeline's does.
func sessionTree(tree string, rowStyle lipgloss.Style) string {
	if tree == "" {
		return ""
	}
	return rowStyle.Foreground(everforest.Faint).Render(tree)
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
