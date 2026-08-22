package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
)

// popupOption is one selectable choice in a floating popup. key (when non-zero)
// is a single-keystroke shortcut; kind colors the intent (used by approval
// popups to distinguish allow from deny).
type popupOption struct {
	label string
	key   rune
	kind  string // "allow" | "deny" | "" (neutral)
}

// popup is a focus-taking overlay drawn on top of the chat. It shows a title, a
// body describing *what* is being asked, and a selectable option list. While a
// popup is open all keyboard input goes to it, not the chat input.
type popup struct {
	title string
	body  []string // pre-wrapped "what" lines
	opts  []popupOption

	sel    int // selected option index
	scroll int // option window scroll offset
}

func newPopup(title string, body []string, opts []popupOption) *popup {
	return &popup{title: title, body: body, opts: opts}
}

// move shifts the selection by delta (clamped); the option window follows.
func (p *popup) move(delta int) {
	if len(p.opts) == 0 {
		return
	}
	p.sel = clamp(p.sel+delta, 0, len(p.opts)-1)
}

// lines renders the popup box as fixed-width lines. The body is truncated and
// the option list scrolls so the box never exceeds maxH rows — on a short
// window it degrades to compact rather than overflowing the screen.
func (p *popup) lines(width, maxH int) []string {
	if width < 8 {
		width = 8
	}
	inner := width - 2

	var out []string

	// top border + title
	title := trunc(p.title, inner-3)
	pad := inner - len(title) - 1 // "┌─ " prefix and "┐" suffix
	if pad < 0 {
		pad = 0
	}
	// Keep both horizontal rules visually identical. The title is intentionally
	// part of the same muted border line rather than introducing a second color.
	out = append(out, cHeader.Sprint("┌─ "+title+strings.Repeat("─", pad)+"┐"))

	// option window height: never exceed maxH total rows
	optH := maxH - 3 // title + bottom border + at least one content row
	if optH < 0 {
		optH = 0
	}
	if optH > len(p.opts) {
		optH = len(p.opts)
	}

	// body fills what remains above the option window
	bodyH := maxH - 3 - optH
	if bodyH < 0 {
		bodyH = 0
	}
	n := min(len(p.body), bodyH)
	for i := 0; i < n; i++ {
		out = append(out, popContentRow(p.body[i], width, cThinking))
	}
	// a blank separator between body and options when both are present
	if n > 0 && optH > 0 && bodyH > n {
		out = append(out, popContentRow("", width, cHeader))
	}

	if len(p.opts) > 0 {
		if p.sel < p.scroll {
			p.scroll = p.sel
		}
		if p.sel >= p.scroll+optH {
			p.scroll = p.sel - optH + 1
		}
		for i := 0; i < optH; i++ {
			oi := p.scroll + i
			if oi >= len(p.opts) {
				break
			}
			o := p.opts[oi]
			line := "  " + o.label
			if oi == p.sel {
				line = "▸ " + o.label
			}
			if o.key != 0 {
				key := " [" + string(o.key) + "]"
				avail := width - 3
				if len(line)+len(key) > avail {
					line = trunc(line, avail-len(key)-1) + "…"
				}
				line = padRight(line, avail-len(key)) + key
			} else {
				line = padRight(line, width-3)
			}
			if oi == p.sel {
				out = append(out, cHeader.Sprint("│")+" "+cPrompt.Sprint("\033[7m"+line+"\033[0m")+cHeader.Sprint("│"))
			} else {
				out = append(out, cHeader.Sprint("│")+" "+cHeader.Sprint(line)+cHeader.Sprint("│"))
			}
		}
	} else {
		// no options: fill the row budget so the box keeps its shape
		for i := 0; i < optH; i++ {
			out = append(out, popContentRow("", width, cHeader))
		}
	}

	out = append(out, cHeader.Sprint("└"+strings.Repeat("─", inner)+"┘"))
	return out
}

// popContentRow renders one body line inside the box: "│ text ... │".
func popContentRow(text string, width int, style *color.Color) string {
	return cHeader.Sprint("│") + " " + style.Sprint(padRight(trunc(text, width-3), width-3)) + cHeader.Sprint("│")
}

func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func padRight(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
