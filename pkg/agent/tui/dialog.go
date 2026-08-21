package tui

import (
	"fmt"
	"io"
	"strings"
)

const dialogMaxWidth = 72

// Notice draws a boxed popup-style dialog to out. Use it for anything that
// "comes up" while the REPL runs — errors, alerts, approvals. It is plain
// stdio, so it just prints the box and lets the caller re-prompt.
func Notice(out io.Writer, title, body string) {
	bodyLines := wrapLines(body, dialogMaxWidth-4)

	width := len(title) + 7
	for _, l := range bodyLines {
		if n := len(l) + 4; n > width {
			width = n
		}
	}
	if width > dialogMaxWidth {
		width = dialogMaxWidth
	}

	if len(title) > width-6 {
		title = title[:width-6]
	}

	fmt.Fprintf(out, "┌─ %s %s┐\n", title, strings.Repeat("─", width-len(title)-5))
	for _, l := range bodyLines {
		fmt.Fprintf(out, "│ %s %s│\n", l, strings.Repeat(" ", width-len(l)-3))
	}
	fmt.Fprintf(out, "└%s┘\n", strings.Repeat("─", width-2))
}

// wrapLines splits s into lines no longer than max, wrapping at word
// boundaries and preserving explicit newlines.
func wrapLines(s string, max int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if len(line) <= max {
			out = append(out, line)
			continue
		}

		cur := ""
		for _, w := range strings.Fields(line) {
			if cur == "" {
				cur = w
			} else if len(cur)+1+len(w) <= max {
				cur += " " + w
			} else {
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}
