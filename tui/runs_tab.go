package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type runsTabOpenMsg struct{ id RunID }
type runsTabStopMsg struct{ id RunID }
type runsTabRestartMsg struct{ id RunID }
type runsTabRemoveMsg struct{ id RunID }

type RunsTabModel struct {
	runs     []RunSnapshot
	selected int
	width    int
	height   int
}

type resourceMetric struct {
	label   string
	detail  string
	percent float64
	warn    bool
}

func NewRunsTabModel(w, h int) RunsTabModel {
	return RunsTabModel{width: w, height: h}
}

func (m RunsTabModel) SetSize(w, h int) RunsTabModel {
	m.width = w
	m.height = h
	return m
}

func (m RunsTabModel) activeCount() int {
	n := 0
	for _, r := range m.runs {
		if !r.Stopped {
			n++
		}
	}
	return n
}

func (m RunsTabModel) SetRuns(runs []RunSnapshot) RunsTabModel {
	m.runs = runs
	if m.selected >= len(m.runs) {
		m.selected = max(len(m.runs)-1, 0)
	}
	return m
}

func (m RunsTabModel) SetSelectedRun(id RunID) RunsTabModel {
	for i, r := range m.runs {
		if r.ID == id {
			m.selected = i
			break
		}
	}
	return m
}

func (m RunsTabModel) selectedRun() (RunSnapshot, bool) {
	if len(m.runs) == 0 || m.selected < 0 || m.selected >= len(m.runs) {
		return RunSnapshot{}, false
	}
	return m.runs[m.selected], true
}

func (m RunsTabModel) Update(msg tea.Msg) (RunsTabModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < len(m.runs)-1 {
			m.selected++
		}
	case "pgup", "ctrl+u":
		m.selected = max(m.selected-m.pageSize(), 0)
	case "pgdown", "ctrl+d":
		m.selected = min(m.selected+m.pageSize(), max(len(m.runs)-1, 0))
	case "enter":
		if r, ok := m.selectedRun(); ok {
			return m, func() tea.Msg { return runsTabOpenMsg{id: r.ID} }
		}
	case "s":
		if r, ok := m.selectedRun(); ok {
			return m, func() tea.Msg { return runsTabStopMsg{id: r.ID} }
		}
	case "r":
		if r, ok := m.selectedRun(); ok {
			return m, func() tea.Msg { return runsTabRestartMsg{id: r.ID} }
		}
	case "x":
		if r, ok := m.selectedRun(); ok {
			return m, func() tea.Msg { return runsTabRemoveMsg{id: r.ID} }
		}
	}
	return m, nil
}

func (m RunsTabModel) View() string {
	t := ActiveTheme
	if len(m.runs) == 0 {
		msg := lipgloss.JoinVertical(lipgloss.Left,
			styleTitle.Render("Runs"),
			"",
			styleMuted.Render("No active runs yet."),
			styleMuted.Render("Launch a profile from the Profiles tab to create one."),
		)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}

	innerW := max(m.width-4, 20)
	innerH := max(m.height-2, 1)
	resourceView := m.resourcesView(innerW)
	divider := styleMuted.Render("  " + strings.Repeat("─", min(max(innerW-2, 1), 100)))
	resourceBlock := divider + "\n" + resourceView
	resourceLines := lipgloss.Height(resourceBlock)
	listAreaH := max(innerH-resourceLines-1, 3)
	listLimit := max(listAreaH-3, 1) // header + divider + page line

	cols := visibleRunColumns(max(innerW-2, 20)) // 2-char selection prefix
	tableW := runColumnsWidth(cols)
	headerLine := "  " + runsHeaderLine(cols)
	rows := []string{styleMuted.Render(headerLine), styleMuted.Render("  " + strings.Repeat("─", min(tableW, max(innerW-2, 1))))}
	start := selectedPageStart(m.selected, listLimit)
	end := min(start+listLimit, len(m.runs))
	for i := start; i < end; i++ {
		r := m.runs[i]
		selected := i == m.selected
		prefix := "  "
		if selected {
			prefix = styleSelected.Render("▶ ")
		}
		rows = append(rows, prefix+runRowLine(r, selected, cols))
	}
	rows = append(rows, m.pageIndicator(start, end, listLimit))
	listSection := strings.Join(rows, "\n")
	spacerLines := max(listAreaH-lipgloss.Height(listSection), 1)
	body := listSection + "\n" + strings.Repeat("\n", spacerLines) + resourceBlock
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Width(max(m.width-2, 1)).
		Height(max(m.height-2, 1)).
		Render(ClampHeight(NewArea(max(m.width-4, 1), max(m.height-3, 1)), body))
	return box
}

func (m RunsTabModel) pageSize() int {
	innerW := max(m.width-4, 20)
	innerH := max(m.height-2, 1)
	resourceBlockLines := lipgloss.Height(styleMuted.Render("─") + "\n" + m.resourcesView(innerW))
	listAreaH := max(innerH-resourceBlockLines-1, 3)
	return max(listAreaH-3, 1)
}

func selectedPageStart(selected, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	return (selected / pageSize) * pageSize
}

func (m RunsTabModel) pageIndicator(start, end, pageSize int) string {
	if len(m.runs) == 0 {
		return ""
	}
	page := 1
	pages := 1
	if pageSize > 0 {
		page = start/pageSize + 1
		pages = (len(m.runs) + pageSize - 1) / pageSize
	}
	text := fmt.Sprintf("  page %d/%d · showing %d-%d of %d", page, pages, start+1, end, len(m.runs))
	return styleMuted.Render(text)
}

func (m RunsTabModel) resourcesView(width int) string {
	system := latestSystemUsage(m.runs)
	title := styleTitle.Render("Resources")
	if strings.TrimSpace(system) == "" {
		return title + "\n" + styleMuted.Render("  warming up metrics…")
	}
	metrics := parseResourceMetrics(system)
	if len(metrics) == 0 {
		return title + "\n" + styleMuted.Render("  "+system)
	}

	barW := max(min((width-34)/2, 24), 8)
	var lines []string
	lines = append(lines, title)
	for i := 0; i < len(metrics); i += 2 {
		left := renderResourceMetric(metrics[i], barW)
		right := ""
		if i+1 < len(metrics) {
			right = renderResourceMetric(metrics[i+1], barW)
		}
		if right != "" && width >= 72 {
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Left, "  ", left, "    ", right))
		} else {
			lines = append(lines, "  "+left)
			if right != "" {
				lines = append(lines, "  "+right)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func latestSystemUsage(runs []RunSnapshot) string {
	for _, r := range runs {
		if strings.TrimSpace(r.SystemUsage) != "" {
			return r.SystemUsage
		}
	}
	return ""
}

func parseResourceMetrics(system string) []resourceMetric {
	var metrics []resourceMetric
	parts := strings.Split(system, "  |  ")
	for _, raw := range parts {
		part := strings.TrimSpace(strings.TrimPrefix(raw, "nvidia-smi "))
		fields := strings.Fields(part)
		if len(fields) < 2 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			if pct, ok := parsePercent(fields[1]); ok {
				metrics = append(metrics, resourceMetric{label: "cpu", detail: fmt.Sprintf("%.0f%%", pct), percent: pct, warn: pct >= 90})
			}
		case fields[0] == "ram" && len(fields) >= 3:
			if pct, ok := parsePercent(fields[2]); ok {
				metrics = append(metrics, resourceMetric{label: "ram", detail: fields[1] + " " + fmt.Sprintf("%.0f%%", pct), percent: pct, warn: pct >= 85})
			}
		case strings.HasPrefix(fields[0], "gpu") && len(fields) >= 3:
			if pct, ok := parsePercent(fields[1]); ok {
				metrics = append(metrics, resourceMetric{label: fields[0], detail: fmt.Sprintf("%.0f%%", pct), percent: pct, warn: pct >= 90})
			}
			if used, total, ok := parseUsedTotal(fields[2]); ok && total > 0 {
				pct := used / total * 100
				metrics = append(metrics, resourceMetric{label: fields[0] + " vram", detail: fields[2], percent: pct, warn: pct >= 85})
			}
		}
	}
	return metrics
}

func renderResourceMetric(m resourceMetric, barW int) string {
	label := lipgloss.NewStyle().Foreground(ActiveTheme.Muted).Width(9).Render(m.label)
	bar := renderResourceBar(m.percent, barW, m.warn)
	detail := lipgloss.NewStyle().Foreground(ActiveTheme.Text).Width(15).Render(truncateRunText(m.detail, 15))
	return lipgloss.JoinHorizontal(lipgloss.Left, label, " ", bar, " ", detail)
}

func renderResourceBar(percent float64, width int, warn bool) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int((percent/100)*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	fillColor := ActiveTheme.Success
	if warn {
		fillColor = ActiveTheme.Error
	} else if percent >= 70 {
		fillColor = ActiveTheme.Secondary
	}
	fill := lipgloss.NewStyle().Foreground(fillColor).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(ActiveTheme.Muted).Render(strings.Repeat("░", width-filled))
	return fill + empty
}

func parsePercent(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v, err == nil
}

func parseUsedTotal(s string) (float64, float64, bool) {
	clean := strings.TrimSuffix(strings.TrimSpace(s), "GiB")
	parts := strings.Split(clean, "/")
	if len(parts) != 2 {
		return 0, 0, false
	}
	used, errUsed := strconv.ParseFloat(parts[0], 64)
	total, errTotal := strconv.ParseFloat(parts[1], 64)
	return used, total, errUsed == nil && errTotal == nil
}

// runColumn describes one runs-table column. Columns with higher prio are
// dropped first when the terminal is too narrow; flex columns absorb any
// leftover width. prio 0 columns are never dropped.
type runColumn struct {
	title  string
	width  int
	flex   bool
	prio   int
	cell   func(r RunSnapshot) string
	styled bool // use the run's status style instead of the row style
}

const maxFlexGrowth = 18

func runColumns() []runColumn {
	return []runColumn{
		{title: "RUN", width: 5, prio: 0, cell: func(r RunSnapshot) string { return fmt.Sprintf("#%d", r.ID) }},
		{title: "STATUS", width: 11, prio: 0, styled: true, cell: runStatusText},
		{title: "PROFILE", width: 14, flex: true, prio: 0, cell: func(r RunSnapshot) string { return r.ProfileName }},
		{title: "MODEL", width: 18, flex: true, prio: 1, cell: func(r RunSnapshot) string { return r.ModelName }},
		{title: "ENGINE", width: 12, prio: 4, cell: func(r RunSnapshot) string {
			if r.EngineName == "" {
				return "—"
			}
			return r.EngineName
		}},
		{title: "UPTIME", width: 8, prio: 2, cell: runUptime},
		{title: "TPS", width: 6, prio: 3, cell: func(r RunSnapshot) string {
			if r.LiveTPS > 0 {
				return fmt.Sprintf("%.1f", r.LiveTPS)
			}
			return "—"
		}},
	}
}

func runColumnsWidth(cols []runColumn) int {
	w := 0
	for i, c := range cols {
		if i > 0 {
			w++ // gap
		}
		w += c.width
	}
	return w
}

// visibleRunColumns drops low-priority columns until the table fits avail,
// then hands leftover width to the flex (name) columns.
func visibleRunColumns(avail int) []runColumn {
	cols := runColumns()
	for runColumnsWidth(cols) > avail {
		drop := -1
		for i, c := range cols {
			if c.prio > 0 && (drop == -1 || c.prio > cols[drop].prio) {
				drop = i
			}
		}
		if drop == -1 {
			break // only never-drop columns left; accept the overflow
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	flexCnt := 0
	for _, c := range cols {
		if c.flex {
			flexCnt++
		}
	}
	if extra := avail - runColumnsWidth(cols); extra > 0 && flexCnt > 0 {
		per := min(extra/flexCnt, maxFlexGrowth)
		for i := range cols {
			if cols[i].flex {
				cols[i].width += per
			}
		}
	}
	return cols
}

func runsHeaderLine(cols []runColumn) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("%-*s", c.width, c.title)
	}
	return strings.Join(parts, " ")
}

func runRowLine(r RunSnapshot, selected bool, cols []runColumn) string {
	base := lipgloss.NewStyle().Foreground(ActiveTheme.Text)
	if selected {
		base = styleSelected
	}
	cells := make([]string, 0, len(cols)*2)
	for i, c := range cols {
		if i > 0 {
			cells = append(cells, " ")
		}
		style := base
		if c.styled {
			style = runStatusStyle(r)
		}
		cells = append(cells, style.Width(c.width).Render(truncateRunText(c.cell(r), c.width)))
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, cells...)
}

func runStatusText(r RunSnapshot) string {
	if r.Stopping {
		return "◌ stopping"
	}
	if r.Stopped {
		if r.ForceKilled {
			return "■ killed"
		}
		if r.ExitErr != nil {
			return "■ failed"
		}
		return "■ stopped"
	}
	return "● running"
}

func runStatusStyle(r RunSnapshot) lipgloss.Style {
	if r.Stopping {
		return lipgloss.NewStyle().Foreground(ActiveTheme.Secondary).Bold(true)
	}
	if r.Stopped {
		if r.ForceKilled || r.ExitErr != nil {
			return lipgloss.NewStyle().Foreground(ActiveTheme.Error).Bold(true)
		}
		return styleMuted
	}
	return lipgloss.NewStyle().Foreground(ActiveTheme.Success).Bold(true)
}

func runUptime(r RunSnapshot) string {
	if r.StartedAt.IsZero() {
		return "—"
	}
	end := time.Now()
	if r.Stopped && !r.StoppedAt.IsZero() {
		end = r.StoppedAt
	}
	return end.Sub(r.StartedAt).Truncate(time.Second).String()
}

func truncateRunText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
