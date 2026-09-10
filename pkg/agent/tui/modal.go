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
	kind       modalKind
	title      string
	body       string
	diffRows   []diffRow
	bodyOffset int
	options    []modalOption
	selected   int
	switching  bool
	required   bool
	approval   *Approval
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
	if m.kind == modalApproval {
		return renderApprovalModal(m, width, height, styles)
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

func renderApprovalModal(m *modalModel, width, height int, styles harnessStyles) string {
	modalStyle, boxWidth, boxHeight, innerWidth, bodyHeight := approvalModalGeometry(m, width, height, styles)
	title := styles.modalTitle.Render(strings.ToUpper(m.title))
	footerText := ansi.Truncate("←/→ action  ·  PgUp/PgDn review  ·  mouse wheel scroll", innerWidth, "…")
	footer := styles.muted.Render(footerText)
	lines := approvalBodyLines(m, innerWidth, styles)
	maxOffset := max(len(lines)-bodyHeight, 0)
	offset := clamp(m.bodyOffset, 0, maxOffset)
	end := min(offset+bodyHeight, len(lines))
	visibleBody := strings.Join(lines[offset:end], "\n")

	rows := []string{title, visibleBody}
	if maxOffset > 0 {
		rows = append(rows, styles.muted.Render(fmt.Sprintf("review lines %d-%d of %d", offset+1, end, len(lines))))
	} else {
		rows = append(rows, "")
	}
	buttons := make([]string, 0, len(m.options))
	for i, option := range m.options {
		accent := everforest.Green
		if option.decision == "deny" {
			accent = everforest.Red
		}
		style := styles.modalOption.Background(everforest.SurfaceAlt).Foreground(accent).Padding(0, 1)
		if i == m.selected {
			style = lipgloss.NewStyle().Background(accent).Foreground(everforest.Background).Bold(true).Padding(0, 1)
		}
		buttons = append(buttons, style.Render(approvalButtonLabel(option)))
	}
	rows = append(rows, strings.Join(buttons, ""))
	rows = append(rows, footer)
	return modalStyle.Width(boxWidth).MaxHeight(boxHeight).Render(strings.Join(rows, "\n"))
}

func approvalModalGeometry(m *modalModel, width, height int, styles harnessStyles) (lipgloss.Style, int, int, int, int) {
	modalStyle := styles.modal
	if modalStyle.GetHorizontalFrameSize() >= width || modalStyle.GetVerticalFrameSize() >= height {
		modalStyle = modalStyle.Padding(0)
	}
	boxWidth := min(max(width-4, 1), 120)
	boxHeight := min(height, 32)
	innerWidth := contentWidth(modalStyle, boxWidth)
	titleHeight := lipgloss.Height(styles.modalTitle.Render(strings.ToUpper(m.title)))
	fixedHeight := titleHeight + 3 // scroll position, action row, and footer
	bodyHeight := max(boxHeight-modalStyle.GetVerticalFrameSize()-fixedHeight, 1)
	return modalStyle, boxWidth, boxHeight, innerWidth, bodyHeight
}

func approvalMaxBodyOffset(m *modalModel, width, height int, styles harnessStyles) int {
	_, _, _, innerWidth, bodyHeight := approvalModalGeometry(m, width, height, styles)
	return max(len(approvalBodyLines(m, innerWidth, styles))-bodyHeight, 0)
}

// approvalBodyLines renders the metadata body followed by the structured diff
// rows (when present) into the scrollable review pane.
func approvalBodyLines(m *modalModel, width int, styles harnessStyles) []string {
	var lines []string
	if m.body != "" {
		lines = append(lines, strings.Split(renderApprovalBody(m.body, width, styles), "\n")...)
	}
	if len(m.diffRows) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		oldWidth, newWidth := diffGutterWidths(m.diffRows)
		codeWidth := max(width-(oldWidth+newWidth+4), 1)
		for _, row := range m.diffRows {
			lines = append(lines, renderDiffRow(row, oldWidth, newWidth, codeWidth, styles)...)
		}
	}
	return lines
}

func renderApprovalBody(body string, width int, styles harnessStyles) string {
	var rendered []string
	section := ""
	for _, line := range strings.Split(body, "\n") {
		style := styles.modalBody.Background(everforest.Surface)
		switch line {
		case "SCRIPT", "NEW CONTENT":
			section = line
			style = styles.active.Background(everforest.Surface).Bold(true)
		case "BEFORE":
			section = line
			style = lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Red).Bold(true)
		case "AFTER":
			section = line
			style = styles.active.Background(everforest.Surface).Bold(true)
		default:
			switch section {
			case "SCRIPT", "NEW CONTENT":
				style = lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text)
			case "BEFORE":
				style = lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Red)
			case "AFTER":
				style = lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Green)
			}
		}
		if section == "NEW CONTENT" {
			if number, content, ok := splitNumberedContent(line); ok {
				numberWidth := lipgloss.Width(number)
				numberStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(lipgloss.Color("8")).Width(numberWidth)
				contentStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text).Width(max(width-numberWidth-2, 1))
				gap := lipgloss.NewStyle().Background(everforest.Surface).Render("  ")
				line = lipgloss.JoinHorizontal(lipgloss.Top, numberStyle.Render(number), gap, contentStyle.Render(content))
				rendered = append(rendered, strings.Split(line, "\n")...)
				continue
			}
		}
		rendered = append(rendered, strings.Split(style.Width(width).Render(line), "\n")...)
	}
	return strings.Join(rendered, "\n")
}

func approvalButtonLabel(option modalOption) string {
	if option.shortcut == 0 || option.label == "" {
		return option.label
	}
	return fmt.Sprintf("[%c]%s", option.shortcut-'a'+'A', option.label[1:])
}

func splitNumberedContent(line string) (string, string, bool) {
	separator := strings.Index(line, "  ")
	if separator <= 0 {
		return "", "", false
	}
	number := line[:separator]
	for _, r := range strings.TrimSpace(number) {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return number, line[separator+2:], true
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
