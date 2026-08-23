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
	label  string
	prefix string
	key    rune
	kind   string // "allow" | "deny" | "" (neutral)
	style  string // timeline role styling: system, user, assistant
}

// popup is a focus-taking overlay drawn on top of the chat. It shows a title, a
// body describing *what* is being asked, and a selectable option list. While a
// popup is open all keyboard input goes to it, not the chat input.
type popup struct {
	title     string
	body      []string // pre-wrapped "what" lines
	opts      []popupOption
	escapable bool

	sel    int // selected option index
	scroll int // option window scroll offset
}

func newPopup(title string, body []string, opts []popupOption, escapable bool) *popup {
	return &popup{title: title, body: body, opts: opts, escapable: escapable}
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
	pad := inner - utf8.RuneCountInString(title) - 1 // "┌─ " prefix and "┐" suffix
	if pad < 0 {
		pad = 0
	}
	// Keep both horizontal rules visually identical. The title is intentionally
	// part of the same muted border line rather than introducing a second color.
	out = append(out, cPopupBorder.Sprint("┌─ "+title+strings.Repeat("─", pad)+"┐"))

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
		out = append(out, popContentRow(p.body[i], width, cChatText))
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
			marker := "  "
			if oi == p.sel {
				marker = "> "
			}
			line := marker + o.prefix + o.label
			if o.key != 0 {
				key := " [" + string(o.key) + "]"
				avail := width - 3
				if utf8.RuneCountInString(line)+utf8.RuneCountInString(key) > avail {
					line = trunc(line, avail-utf8.RuneCountInString(key)-1) + "…"
				}
				line = padRight(line, avail-utf8.RuneCountInString(key)) + key
			} else {
				line = trunc(line, width-3)
				line = padRight(line, width-3)
			}
			if oi == p.sel {
				prefix := marker + o.prefix
				label := strings.TrimPrefix(line, prefix)
				styled := cPopupBorder.Sprint("> ") + cThinking.Sprint(o.prefix) + timelineOptionStyle(o.style).Sprint(label)
				out = append(out, cPopupBorder.Sprint("│")+" "+styled+cPopupBorder.Sprint("│"))
			} else {
				prefix := "  " + o.prefix
				label := strings.TrimPrefix(line, prefix)
				out = append(out, cPopupBorder.Sprint("│")+" "+cThinking.Sprint(prefix)+timelineOptionStyle(o.style).Sprint(label)+cPopupBorder.Sprint("│"))
			}
		}
	} else {
		// no options: fill the row budget so the box keeps its shape
		for i := 0; i < optH; i++ {
			out = append(out, popContentRow("", width, cChatText))
		}
	}

	out = append(out, cPopupBorder.Sprint("└"+strings.Repeat("─", inner)+"┘"))
	return out
}

func timelineOptionStyle(role string) *color.Color {
	switch role {
	case "system":
		return cSystem
	case "user":
		return cUser
	case "assistant":
		return cAssistant
	default:
		return cChatText
	}
}

// popContentRow renders one body line inside the box: "│ text ... │".
func popContentRow(text string, width int, style *color.Color) string {
	return cPopupBorder.Sprint("│") + " " + style.Sprint(padRight(trunc(text, width-3), width-3)) + cPopupBorder.Sprint("│")
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
