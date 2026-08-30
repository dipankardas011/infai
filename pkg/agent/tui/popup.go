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
	label        string
	prefix       string
	key          rune
	kind         string // "allow" | "deny" | "" (neutral)
	style        string // timeline role styling: system, user, assistant
	marker       string // optional timeline marker, such as "▲ " or "▲▲ "
	markerStatus string // result marker status: success or error
	toolName     string // tool name receives the purple tool accent
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

	left, top int
	optTop    int
	optH      int
	mouseSel  int
}

func newPopup(title string, body []string, opts []popupOption, escapable bool) *popup {
	return &popup{title: title, body: body, opts: opts, escapable: escapable, mouseSel: -1}
}

func (p *popup) setGeometry(left, top, maxH int) {
	p.left = left
	p.top = top
	p.optH = min(max(maxH-3, 0), len(p.opts))
	bodyH := max(maxH-3-p.optH, 0)
	n := min(len(p.body), bodyH)
	p.optTop = top + 1 + n
	if n > 0 && p.optH > 0 && bodyH > n {
		p.optTop++
	}
}

func (p *popup) optionAt(x, y int) (int, bool) {
	if x < p.left || y < p.optTop || y >= p.optTop+p.optH {
		return 0, false
	}
	idx := p.scroll + y - p.optTop
	if idx < 0 || idx >= len(p.opts) {
		return 0, false
	}
	return idx, true
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
	pad := max(inner-utf8.RuneCountInString(title)-1, 0) // "┌─ " prefix and "┐" suffix
	// Keep both horizontal rules visually identical. The title is intentionally
	// part of the same muted border line rather than introducing a second color.
	out = append(out, cPopupBorder.Sprint("┌─ "+title+strings.Repeat("─", pad)+"┐"))

	// option window height: never exceed maxH total rows
	optH := min(max(maxH-3, 0), len(p.opts))

	// body fills what remains above the option window
	bodyH := max(maxH-3-optH, 0)
	n := min(len(p.body), bodyH)
	for i := range n {
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
		for i := range optH {
			oi := p.scroll + i
			if oi >= len(p.opts) {
				break
			}
			o := p.opts[oi]
			keyText := ""
			if o.key != 0 {
				keyText = " [" + string(o.key) + "]"
			}
			contentWidth := max(width-3-utf8.RuneCountInString(keyText), 0)
			prefix := trunc(o.prefix, contentWidth)
			remaining := max(contentWidth-utf8.RuneCountInString(prefix), 0)
			markerText := trunc(o.marker, remaining)
			remaining = max(remaining-utf8.RuneCountInString(markerText), 0)
			label := padRight(trunc(o.label, remaining), remaining)
			if oi == p.sel {
				styled := cPopupBorder.Sprint("> ") + cThinking.Sprint(prefix) +
					renderTimelineMarker(o, markerText) + renderTimelineLabel(o, label) + cThinking.Sprint(keyText)
				out = append(out, cPopupBorder.Sprint("│")+" "+styled+cPopupBorder.Sprint("│"))
			} else {
				styled := cThinking.Sprint("  "+prefix) + renderTimelineMarker(o, markerText) + renderTimelineLabel(o, label) + cThinking.Sprint(keyText)
				out = append(out, cPopupBorder.Sprint("│")+" "+styled+cPopupBorder.Sprint("│"))
			}
		}
	} else {
		// no options: fill the row budget so the box keeps its shape
		for range optH {
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
	case "tool_call":
		return cToolCallText
	case "tool_result_success", "tool_result_error":
		return cToolResultText
	default:
		return cChatText
	}
}

func renderTimelineLabel(option popupOption, label string) string {
	style := timelineOptionStyle(option.style)
	if option.toolName == "" {
		return style.Sprint(label)
	}
	nameStart := strings.Index(label, option.toolName)
	if nameStart < 0 {
		return style.Sprint(label)
	}
	nameEnd := nameStart + len(option.toolName)
	return style.Sprint(label[:nameStart]) + cSystem.Sprint(option.toolName) + style.Sprint(label[nameEnd:])
}

func renderTimelineMarker(option popupOption, marker string) string {
	if marker == "" {
		return ""
	}
	if option.markerStatus == "success" && utf8.RuneCountInString(marker) >= 2 {
		runes := []rune(marker)
		return cSystem.Sprint(string(runes[:1])) + cAssistant.Sprint(string(runes[1:2])) + cSystem.Sprint(string(runes[2:]))
	}
	if option.markerStatus != "" && utf8.RuneCountInString(marker) >= 2 {
		runes := []rune(marker)
		return cSystem.Sprint(string(runes[:1])) + cError.Sprint(string(runes[1:2])) + cSystem.Sprint(string(runes[2:]))
	}
	return cSystem.Sprint(marker)
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
