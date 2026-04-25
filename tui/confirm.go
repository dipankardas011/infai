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
	height     int
}

func NewConfirmModel(serverBin string, m model.ModelEntry, p model.Profile, w, h int) ConfirmModel {
	return ConfirmModel{
		serverBin:  serverBin,
		modelEntry: m,
		profile:    p,
		command:    launcher.BuildCommand(serverBin, m, p),
		width:      w,
		height:     h,
	}
}

func (c ConfirmModel) SetSize(w, h int) ConfirmModel {
	c.width = w
	c.height = h
	c.command = launcher.BuildCommand(c.serverBin, c.modelEntry, c.profile)
	return c
}

func (c ConfirmModel) Update(_ tea.Msg) (ConfirmModel, tea.Cmd) { return c, nil }

func (c ConfirmModel) View() string {
	t := ActiveTheme

	// Use a responsive width for the command box
	targetW := 60
	if c.width < 64 {
		targetW = c.width - 8
	}
	if targetW < 30 {
		targetW = 30
	}

	wrapped := formatCommand(c.Args(), targetW-8)

	title := styleTitle.Render("Ready to launch")

	modelLine := styleMuted.Render("model:   ") +
		lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(c.modelEntry.DisplayName)
	profileLine := styleMuted.Render("profile: ") +
		lipgloss.NewStyle().Foreground(t.Secondary).Bold(true).Render(c.profile.Name)

	cmdBox := styleBox.Width(targetW).Render(
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
		fmt.Sprintf("llama-server will run inside the TUI"),
	)

	content := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"\n"+modelLine,
		profileLine,
		"\n"+cmdBox,
		"\n"+enterHint+escHint,
		"\n"+note,
	)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2)

	return lipgloss.Place(c.width, c.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (c ConfirmModel) Args() []string {
	return launcher.BuildArgs(c.serverBin, c.modelEntry, c.profile)
}

func formatCommand(args []string, width int) string {
	if len(args) == 0 {
		return ""
	}

	var lines []string
	// Binary on first line
	binLine := args[0]
	if len(binLine) > width && width > 0 {
		binLine = binLine[:width-3] + "..."
	}
	lines = append(lines, binLine+" \\")

	for i := 1; i < len(args); i++ {
		arg := args[i]
		displayArg := arg
		if strings.ContainsAny(arg, " \t") {
			displayArg = fmt.Sprintf("%q", arg)
		}

		// Check if this is a flag or a value
		isFlag := strings.HasPrefix(arg, "-")

		var line string
		if isFlag {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				val := args[i+1]
				displayVal := val
				if strings.ContainsAny(val, " \t") {
					displayVal = fmt.Sprintf("%q", val)
				}
				line = fmt.Sprintf("  %s %s", displayArg, displayVal)
				i++
			} else {
				line = fmt.Sprintf("  %s", displayArg)
			}
		} else {
			line = fmt.Sprintf("  %s", displayArg)
		}

		// If the combined flag+value line is too long, we must wrap it or truncate it
		if width > 0 && len(line) > width {
			line = line[:width-3] + "..."
		}

		if i < len(args)-1 {
			line += " \\"
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}
