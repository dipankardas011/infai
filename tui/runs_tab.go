package tui

import (
	"fmt"
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

func NewRunsTabModel(w, h int) RunsTabModel {
	return RunsTabModel{width: w, height: h}
}

func (m RunsTabModel) SetSize(w, h int) RunsTabModel {
	m.width = w
	m.height = h
	return m
}

func (m RunsTabModel) SetRuns(runs []RunSnapshot) RunsTabModel {
	m.runs = runs
	if m.selected >= len(m.runs) {
		m.selected = max(len(m.runs)-1, 0)
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
	headerLine := "  " + runsHeaderLine()
	rows := []string{styleMuted.Render(headerLine), styleMuted.Render("  " + strings.Repeat("─", min(innerW-2, 100)))}
	limit := max(innerH-2, 1)
	start := 0
	if m.selected >= limit {
		start = m.selected - limit + 1
	}
	end := min(start+limit, len(m.runs))
	for i := start; i < end; i++ {
		r := m.runs[i]
		selected := i == m.selected
		prefix := "  "
		if selected {
			prefix = styleSelected.Render("▶ ")
		}
		rows = append(rows, prefix+runRowLine(r, selected))
	}
	footer := styleMuted.Render("enter:view  s:stop  r:restart  x:remove stopped  ↑/↓:select")
	body := strings.Join(rows, "\n")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Width(max(m.width-2, 1)).
		Height(max(m.height-2, 1)).
		Render(ClampHeight(NewArea(max(m.width-4, 1), max(m.height-3, 1)), body+"\n\n"+footer))
	return box
}

func runsHeaderLine() string {
	return fmt.Sprintf("%-5s %-11s %-22s %-26s %-7s %-9s %-8s", "RUN", "STATUS", "PROFILE", "MODEL", "PORT", "UPTIME", "TPS")
}

func runRowLine(r RunSnapshot, selected bool) string {
	base := lipgloss.NewStyle().Foreground(ActiveTheme.Text)
	if selected {
		base = styleSelected
	}
	id := base.Width(5).Render(fmt.Sprintf("#%d", r.ID))
	status := runStatusStyle(r).Width(11).Render(runStatusText(r))
	profile := base.Width(22).Render(truncateRunText(r.ProfileName, 22))
	model := base.Width(26).Render(truncateRunText(r.ModelName, 26))
	port := base.Width(7).Render(fmt.Sprintf(":%d", r.ActualPort))
	uptime := base.Width(9).Render(runUptime(r))
	tps := "—"
	if r.LiveTPS > 0 {
		tps = fmt.Sprintf("%.1f", r.LiveTPS)
	}
	tpsCell := base.Width(8).Render(tps)
	return lipgloss.JoinHorizontal(lipgloss.Left, id, " ", status, " ", profile, " ", model, " ", port, " ", uptime, " ", tpsCell)
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
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}
