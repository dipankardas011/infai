package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
)

type modalKind int

const (
	modalSessions modalKind = iota
	modalModels
	modalCommands
	modalApproval
	modalTimeline
	modalNotice
)

type modalOption struct {
	label    string
	role     string
	detail   string
	status   string
	current  bool
	tree     string
	fork     string
	shortcut rune
	command  string
	provider string
	model    string
	session  uuid.UUID
	event    *TimelineEvent
	decision string
}

type modalModel struct {
	kind      modalKind
	title     string
	body      string
	options   []modalOption
	selected  int
	switching bool
	required  bool
	approval  *Approval
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

func timelineRoleStyle(base lipgloss.Style, role string) lipgloss.Style {
	color := "10" // assistant: HiGreen
	switch role {
	case "user":
		color = "4" // FgBlue
	case "thinking":
		color = "8" // HiBlack
	case "tool_call", "tool_result":
		color = "13" // HiPurple
	case "skill":
		color = "6" // cyan
	}
	return base.Foreground(lipgloss.Color(color))
}

func renderSelectionScreen(m *modalModel, width, height int, styles harnessStyles) string {
	if m == nil || width <= 0 || height <= 0 {
		return ""
	}
	if m.kind == modalSessions && width >= 50 && height >= 22 {
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
		status := ""
		rowStyle := styles.screenRow
		prefix := "  "
		if i == m.selected {
			rowStyle = styles.screenSel
			prefix = "› "
			if option.status != "" {
				status = "[" + option.status + "]  "
			}
		} else if option.status != "" {
			statusStyle := styles.inactive
			if option.status == "ACTIVE" || option.status == "NEW" || option.status == "SUCCESS" || option.status == "HEAD" {
				statusStyle = styles.active
			}
			status = statusStyle.Render("["+option.status+"]") + "  "
		}
		line := status + option.label
		if option.detail != "" {
			if i == m.selected {
				line += "  " + option.detail
			} else {
				line += styles.inactive.Render("  " + option.detail)
			}
		}
		line = ansi.Truncate(prefix+line, contentWidth, "…")
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
	footerText := "n new  ·  ↑/↓ navigate sessions  ·  enter open"
	if !m.required {
		footerText += "  ·  esc back"
	}
	footer := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, styles.inactive.Render(footerText))

	contentHeight := max(height-lipgloss.Height(header)-lipgloss.Height(footer), 0)
	innerWidth := max(width-4, 1)
	var main string
	if width >= 90 {
		main = renderWideSessionWorkspace(m, innerWidth, contentHeight, styles)
	} else {
		main = renderStackedSessionWorkspace(m, innerWidth, contentHeight, styles)
	}
	if pad := contentHeight - lipgloss.Height(main); pad > 0 {
		main += strings.Repeat("\n", pad)
	}
	body := lipgloss.NewStyle().Padding(0, 2).Render(main)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func renderWideSessionWorkspace(m *modalModel, width, height int, styles harnessStyles) string {
	gap := 2
	leftWidth := max((width-gap)*2/5, 30)
	rightWidth := max(width-gap-leftWidth, 1)

	newPanel := renderNewSessionPanel(m, leftWidth, styles)
	leftMax := max(height-lipgloss.Height(newPanel)-6, 0)
	active := renderActiveSessionsPanel(m, leftWidth, leftMax, styles)
	left := newPanel + "\n" + active
	right := renderAllSessionsPanel(m, rightWidth, height, styles)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
}

func renderStackedSessionWorkspace(m *modalModel, width, height int, styles harnessStyles) string {
	newPanel := renderNewSessionPanel(m, width, styles)
	leftMax := max(height-lipgloss.Height(newPanel)-6, 0)
	active := renderActiveSessionsPanel(m, width, leftMax, styles)
	allHeight := max(height-lipgloss.Height(newPanel)-lipgloss.Height(active)-3, 6)
	all := renderAllSessionsPanel(m, width, allHeight, styles)
	return strings.Join([]string{newPanel, active, all}, "\n")
}

func renderNewSessionPanel(m *modalModel, width int, styles harnessStyles) string {
	selected := m.selected == 0
	actionStyle := styles.screenRow
	border := everforest.SurfaceAlt
	prefix := "  "
	if selected {
		actionStyle = styles.screenSel
		border = everforest.Green
		prefix = "› "
	}
	contentWidth := max(width-4, 1)
	action := sessionRow(actionStyle, prefix+"+ New session", contentWidth)
	hint := styles.inactive.Render(ansi.Truncate("  choose provider and model", contentWidth, "…"))
	return renderSessionPanel("NEW SESSION", action+"\n"+hint, width, 0, border, styles)
}

func renderActiveSessionsPanel(m *modalModel, width, maxLines int, styles harnessStyles) string {
	active := activeSessionOptions(m.options)
	contentWidth := max(width-4, 1)
	lines := make([]string, 0, max(maxLines, 1))
	if len(active) == 0 {
		lines = append(lines, styles.inactive.Render("No active sessions"))
	} else {
		for i, option := range active {
			if i >= maxLines {
				break
			}
			lines = append(lines, sessionRow(styles.active, "● "+option.label, contentWidth))
		}
	}
	return renderSessionPanel("ACTIVE SESSIONS", strings.Join(lines, "\n"), width, 0, everforest.Aqua, styles)
}

func renderAllSessionsPanel(m *modalModel, width, height int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	options := m.options
	if len(options) > 0 {
		options = options[1:]
	}
	if len(options) == 0 {
		return renderSessionPanel("ALL SESSIONS", styles.inactive.Render("No saved sessions yet"), width, height, everforest.SurfaceAlt, styles)
	}
	selected := clamp(m.selected-1, 0, len(options)-1)
	rowCap := max((height-5)/2, 1)
	start, end := visibleRange(len(options), selected, rowCap)
	rows := make([]string, 0, (end-start)*2+1)
	for i := start; i < end; i++ {
		option := options[i]
		isSelected := m.selected == i+1
		prefix := "  "
		nameStyle := styles.screenRow
		if option.status == "ACTIVE" {
			nameStyle = styles.active
		}
		if isSelected {
			prefix = "› "
			nameStyle = styles.screenSel
		}
		nameLine := sessionRow(nameStyle, prefix+option.label, contentWidth)
		detailStyle := styles.inactive
		if isSelected {
			detailStyle = styles.inactive.Background(everforest.SurfaceAlt)
		}
		detailLine := sessionRow(detailStyle, "    "+option.detail, contentWidth)
		rows = append(rows, nameLine+"\n"+detailLine)
	}
	if start > 0 || end < len(options) {
		rows = append(rows, styles.inactive.Render(fmt.Sprintf("%d-%d of %d", start+1, end, len(options))))
	}
	border := everforest.SurfaceAlt
	if m.selected > 0 {
		border = everforest.Green
	}
	return renderSessionPanel("ALL SESSIONS", strings.Join(rows, "\n"), width, height, border, styles)
}

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

func activeSessionOptions(options []modalOption) []modalOption {
	active := make([]modalOption, 0)
	for _, option := range options {
		if option.status == "ACTIVE" && option.session != uuid.Nil {
			active = append(active, option)
		}
	}
	return active
}
