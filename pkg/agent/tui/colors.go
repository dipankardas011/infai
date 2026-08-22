package tui

import "github.com/fatih/color"

// Color scheme for the plain-stdio REPL. fatih/color disables the ANSI codes
// automatically when the output is not a terminal, so piping stays plain.
var (
	cPrompt    = color.New(color.FgBlue)             // the "> " input marker
	cUser      = color.New(color.FgBlue, color.Bold) // user messages (● dot, blue)
	cThinking  = color.New(color.FgHiBlack)          // model reasoning (dark grey)
	cAssistant = color.New(color.FgGreen)            // model answers (● dot, green)
	cHeader    = color.New(color.FgHiBlack)          // footer + separators
	cSystem    = color.New(color.FgYellow)           // system/notices
	cError     = color.New(color.FgRed)              // errors
)
