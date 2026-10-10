package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type commandSpec struct {
	name string
	// args is the argument hint rendered after the name; empty when the
	// command takes none.
	args string
	help string
}

// display is the menu row's command column: the name plus its argument hint.
func (c commandSpec) display() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

// insertion is what the composer receives when the command is picked. A command
// carrying arguments gets a trailing space so the next keystroke starts the
// argument list instead of running into the command name.
func (c commandSpec) insertion() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " "
}

// promptArgumentHint renders an MCP prompt's arguments in the key=value syntax
// the composer parses: required as "who=", optional as "[tone=]".
func promptArgumentHint(arguments []contracts.MCPPromptArgument) string {
	hints := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument.Name == "" {
			continue
		}
		hint := argument.Name + "="
		if !argument.Required {
			hint = "[" + hint + "]"
		}
		hints = append(hints, hint)
	}
	return strings.Join(hints, " ")
}

var harnessCommands = []commandSpec{
	{name: "/new", help: "start a clean session"},
	{name: "/sessions", help: "open another session"},
	{name: "/model", help: "switch the active model"},
	{name: "/rename", args: "<name>", help: "rename the active session"},
	{name: "/compact", help: "compact conversation context"},
	{name: "/timeline", help: "branch from an earlier event"},
	{name: "/help", help: "show keyboard help"},
	{name: "/quit", help: "leave the harness"},
}

func matchingCommands(input string, prompts []contracts.MCPPrompt) []commandSpec {
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \n\t") {
		return nil
	}
	var matches []commandSpec
	for _, command := range harnessCommands {
		if strings.HasPrefix(command.name, input) {
			matches = append(matches, command)
		}
	}
	for _, prompt := range prompts {
		name := "/" + prompt.Server + ":" + prompt.Name
		if strings.HasPrefix(name, input) {
			matches = append(matches, commandSpec{name: name, args: promptArgumentHint(prompt.Arguments), help: prompt.Description})
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
		menuWidth = max(menuWidth, lipgloss.Width("  "+command.display()+"  "+command.help))
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
		line := ansi.Truncate(prefix+command.display()+"  "+command.help, innerWidth, "…")
		rows = append(rows, style.Width(innerWidth).Render(line))
	}
	return styles.menu.Width(menuWidth).Render(strings.Join(rows, "\n"))
}
