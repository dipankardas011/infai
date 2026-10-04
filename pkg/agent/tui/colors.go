package tui

import "github.com/fatih/color"

// The plain-stdio REPL's palette. This file is the only place in the REPL that
// names a colour: every other file uses these, and theme.go holds the palette
// the bubbletea UI draws with.
//
// The two palettes cannot be the same values — fatih/color takes ANSI
// attributes and the theme uses truecolor — so each entry below names the
// theme colour it stands in for. fatih/color drops the ANSI codes when the
// output is not a terminal, so piping stays plain.
var (
	cPrompt     = color.New(color.FgBlue)             // the "> " input marker
	cUser       = color.New(color.FgBlue, color.Bold) // user messages (● dot), theme: Blue
	cThinking   = color.New(color.FgHiBlack)          // model reasoning (dark grey), theme: Muted
	cAssistant  = color.New(color.FgGreen)            // model answers (● dot, green), theme: Green
	cHeader     = color.New(color.FgHiBlack)          // footer + separators
	cSystem     = color.New(color.FgMagenta)          // system/notices, theme: Purple
	cToolCall   = color.New(color.FgMagenta)          // tool calls and their arguments, theme: Purple
	cToolResult = color.New(color.FgYellow)           // tool results and their output, theme: Orange
	cTree       = color.New(color.FgHiBlack)          // the timeline's tree guides, theme: Faint
	cSkill      = color.New(color.FgHiCyan)           // skill loads, theme: Aqua
)

// timelineRoleColor is the timeline's role palette, used by both the
// branch-selection screen here and the timeline modal. The roles are the ones
// timelineEventDisplays emits; a role with no colour of its own returns nil so
// the caller renders it plainly instead of borrowing another role's colour.
func timelineRoleColor(role string) *color.Color {
	switch role {
	case "user":
		return cUser
	case "assistant":
		return cAssistant
	case "thinking":
		return cThinking
	case "system":
		return cSystem
	case "tool_call":
		return cToolCall
	case "tool_result":
		return cToolResult
	case "skill":
		return cSkill
	default:
		return nil
	}
}
