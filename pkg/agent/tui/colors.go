package tui

import "github.com/fatih/color"

// Color scheme for the plain-stdio REPL. fatih/color disables the ANSI codes
// automatically when the output is not a terminal, so piping stays plain.
var (
	cPrompt         = color.New(color.FgBlue)              // the "> " input marker
	cUser           = color.New(color.FgBlue, color.Bold)  // sent user messages
	cThinking       = color.New(color.FgHiBlack)           // model reasoning (dark grey)
	cAssistant      = color.New(color.FgGreen)             // model answers
	cAssistantLabel = color.New(color.FgGreen, color.Bold) // the "assistant: " marker
	cHeader         = color.New(color.FgHiBlack)           // status header + separators
	cSystem         = color.New(color.FgYellow)            // system/notices
	cError          = color.New(color.FgRed)               // errors
)
