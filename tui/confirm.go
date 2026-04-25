package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/launcher"
	"github.com/dipankardas011/infai/model"
)

// ConfirmModel is screen 4 — shows the assembled command before launch.
type ConfirmModel struct {
	serverBin  string
	modelEntry model.ModelEntry
	profile    model.Profile
	command    string
	width      int
}

func NewConfirmModel(serverBin string, m model.ModelEntry, p model.Profile, w int) ConfirmModel {
	return ConfirmModel{
		serverBin:  serverBin,
		modelEntry: m,
		profile:    p,
		command:    launcher.BuildCommand(serverBin, m, p),
		width:      w,
	}
}

func (c ConfirmModel) SetWidth(w int) ConfirmModel {
	c.width = w
	c.command = launcher.BuildCommand(c.serverBin, c.modelEntry, c.profile)
	return c
}

func (c ConfirmModel) Update(_ tea.Msg) (ConfirmModel, tea.Cmd) { return c, nil }

func (c ConfirmModel) View() string {
	t := ActiveTheme
	boxW := c.width - 6
	if boxW < 40 {
		boxW = 40
	}

	title := styleTitle.Render("Ready to launch")

	modelLine := styleMuted.Render("model:   ") +
		lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(c.modelEntry.DisplayName)
	profileLine := styleMuted.Render("profile: ") +
		lipgloss.NewStyle().Foreground(t.Secondary).Bold(true).Render(c.profile.Name)

	wrapped := wordWrap(c.command, boxW-6)
	cmdBox := styleBox.Width(boxW).Render(
		styleMuted.Render("$ ") + lipgloss.NewStyle().Foreground(t.Primary).Render(wrapped),
	)

	// Prominent enter hint
	enterHint := lipgloss.NewStyle().
		Foreground(t.Bg).
		Background(t.Success).
		Bold(true).
		Padding(0, 2).
		Render("  ENTER  to launch")
	escHint := lipgloss.NewStyle().
		Foreground(t.Muted).
		Render("  ESC to go back")

	note := lipgloss.NewStyle().Foreground(t.Muted).Italic(true).Render(
		fmt.Sprintf("llama-server will run inside the TUI — press s to stop it"),
	)

	return title + "\n\n" +
		modelLine + "\n" +
		profileLine + "\n\n" +
		cmdBox + "\n\n" +
		enterHint + escHint + "\n\n" +
		note
}

func (c ConfirmModel) Args() []string {
	return launcher.BuildArgs(c.serverBin, c.modelEntry, c.profile)
}

func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if len(cur)+1+len(w) <= width {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = "  " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}
