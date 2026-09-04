package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type commandSpec struct {
	name string
	help string
}

var harnessCommands = []commandSpec{
	{name: "/new", help: "start a clean session"},
	{name: "/sessions", help: "open another session"},
	{name: "/model", help: "switch the active model"},
	{name: "/compact", help: "compact conversation context"},
	{name: "/timeline", help: "branch from an earlier event"},
	{name: "/help", help: "show keyboard help"},
	{name: "/quit", help: "leave the harness"},
}

func matchingCommands(input string) []commandSpec {
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \n\t") {
		return nil
	}
	var matches []commandSpec
	for _, command := range harnessCommands {
		if strings.HasPrefix(command.name, input) {
			matches = append(matches, command)
		}
	}
	return matches
}

func renderCommandMenu(commands []commandSpec, selected, width, height int, styles harnessStyles) string {
	if len(commands) == 0 {
		return ""
	}
	menuWidth := 0
	for _, command := range commands {
		menuWidth = max(menuWidth, lipgloss.Width("  "+command.name+"  "+command.help))
	}
	menuWidth = min(menuWidth+styles.menu.GetHorizontalFrameSize(), width)
	innerWidth := contentWidth(styles.menu, menuWidth)
	start, end := visibleRange(len(commands), selected, height)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		command := commands[i]
		style := styles.menuRow
		prefix := "  "
		if i == selected {
			style = styles.menuActive
			prefix = "› "
		}
		line := ansi.Truncate(prefix+command.name+"  "+command.help, innerWidth, "…")
		rows = append(rows, style.Width(innerWidth).Render(line))
	}
	return styles.menu.Width(menuWidth).Render(strings.Join(rows, "\n"))
}
