package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type resourcePicker struct {
	resources []contracts.MCPResource
	matches   []contracts.MCPResource
	query     string
	start     int
	selected  int
}

func fuzzyResourceMatches(resources []contracts.MCPResource, query string, limit int) []contracts.MCPResource {
	type match struct {
		resource contracts.MCPResource
		score    int
	}
	query = strings.ToLower(query)
	var found []match
	for _, resource := range resources {
		lower, pos, score := strings.ToLower(resource.URI), 0, 0
		ok := true
		for _, char := range query {
			i := strings.IndexRune(lower[pos:], char)
			if i < 0 {
				ok = false
				break
			}
			pos += i + 1
			score += i
		}
		if ok {
			found = append(found, match{resource, score})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score < found[j].score
		}
		return len(found[i].resource.URI) < len(found[j].resource.URI)
	})
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	out := make([]contracts.MCPResource, len(found))
	for i := range found {
		out[i] = found[i].resource
	}
	return out
}

func (p *resourcePicker) filter(query string) {
	p.query = query
	p.matches = fuzzyResourceMatches(p.resources, query, 12)
	p.selected = min(p.selected, max(len(p.matches)-1, 0))
}

func resourceRow(resource contracts.MCPResource) string {
	name := resource.Name
	if name == "" {
		name = resource.URI
	}
	row := name + " · " + resource.URI
	if resource.MIMEType != "" {
		row += " · " + resource.MIMEType
	}
	if resource.Size != 0 {
		row += " · " + humanizeResourceSize(resource.Size)
	}
	return row
}

func humanizeResourceSize(size int64) string {
	switch {
	case size < 1<<10:
		return fmt.Sprintf("%d B", size)
	case size < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	}
}

func renderResourcePicker(p *resourcePicker, width int, styles harnessStyles) string {
	if p == nil || len(p.matches) == 0 {
		return ""
	}
	rows := make([]string, 0, len(p.matches))
	menuWidth := 0
	for _, resource := range p.matches {
		row := resourceRow(resource)
		rows = append(rows, row)
		menuWidth = max(menuWidth, lipgloss.Width("  "+row))
	}
	menuWidth = min(menuWidth+styles.menu.GetHorizontalFrameSize(), width)
	innerWidth := contentWidth(styles.menu, menuWidth)
	for i, row := range rows {
		prefix := "  "
		style := styles.menuRow
		if i == p.selected {
			prefix = "› "
			style = styles.menuActive
		}
		rows[i] = style.Width(innerWidth).Render(truncateLine(prefix+row, innerWidth))
	}
	return styles.menu.Width(menuWidth).Render(strings.Join(rows, "\n"))
}

// renderResourceBlocks collapses the resource blocks the session writes into
// the reference the user typed: #uri, never the body. A body the current
// encoder wrapped in CDATA is dropped whole; a body stored by an older session
// (plain text, or the close token marked with a leading backslash) stays inside
// the element and is skipped here too.
func renderResourceBlocks(text string) string {
	const (
		openTag  = "<infaiw_mcp_resource_get"
		closeTag = "</infaiw_mcp_resource_get>"
		cdataTag = "<![CDATA["
	)
	var out strings.Builder
	for {
		start := unescapedResourceToken(text, openTag)
		if start < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:start])
		end := strings.IndexByte(text[start:], '>')
		if end < 0 {
			out.WriteString(text[start:])
			return out.String()
		}
		end += start
		tag := text[start : end+1]
		uri := resourceTagURI(tag)
		// A self-closing open tag, or one carrying an error attribute, is the
		// read that failed: there is no body to drop, and the reference has to
		// say so.
		if strings.HasSuffix(tag, "/>") || strings.Contains(tag, ` error="`) {
			out.WriteString("#")
			out.WriteString(uri)
			out.WriteString(" (unavailable)")
			text = text[end+1:]
			// The current encoder writes an explicit close tag, never a
			// self-closing element, so consume it when it is there.
			if strings.HasPrefix(text, closeTag) {
				text = text[len(closeTag):]
			}
			continue
		}
		body := text[end+1:]
		if strings.HasPrefix(body, cdataTag) {
			rest, ok := consumeCDATABody(body, closeTag)
			if !ok {
				out.WriteString(text[start:])
				return out.String()
			}
			out.WriteString("#")
			out.WriteString(uri)
			text = rest
			continue
		}
		// Legacy shape: a plain body whose framing tokens the old encoder
		// marked with a backslash.
		closeAt := unescapedResourceToken(body, closeTag)
		if closeAt < 0 {
			out.WriteString(text[start:])
			return out.String()
		}
		out.WriteString("#")
		out.WriteString(uri)
		text = body[closeAt+len(closeTag):]
	}
}

// consumeCDATABody skips a CDATA body and the element's close tag, returning the
// text that follows. The XML encoder splits a body at every `]]>` it contains
// into another CDATA section, so it keeps taking segments until a section is
// followed by the close tag instead of another `<![CDATA[`.
func consumeCDATABody(body, closeTag string) (string, bool) {
	const (
		cdataOpen  = "<![CDATA["
		cdataClose = "]]>"
	)
	for {
		end := strings.Index(body, cdataClose)
		if end < 0 {
			return "", false
		}
		rest := body[end+len(cdataClose):]
		if strings.HasPrefix(rest, cdataOpen) {
			body = rest[len(cdataOpen):]
			continue
		}
		if !strings.HasPrefix(rest, closeTag) {
			return "", false
		}
		return rest[len(closeTag):], true
	}
}

// resourceTagURI reads the uri attribute out of an open tag, undoing the
// escaping the encoder applied to it.
func resourceTagURI(tag string) string {
	const attribute = `uri="`
	_, after, ok := strings.Cut(tag, attribute)
	if !ok {
		return ""
	}
	value := after
	end := strings.IndexByte(value, '"')
	if end < 0 {
		return ""
	}
	return unescapeResourceAttribute(value[:end])
}

// unescapeResourceAttribute reverses the XML escaping of an attribute value:
// the named entities first, then the numeric character references the marshaler
// emits for a delimiter like a double quote (`&#34;`).
func unescapeResourceAttribute(value string) string {
	for _, entity := range [][2]string{{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&apos;", "'"}} {
		value = strings.ReplaceAll(value, entity[0], entity[1])
	}
	var out strings.Builder
	out.Grow(len(value))
	for {
		start := strings.Index(value, "&#")
		if start < 0 {
			break
		}
		semicolon := strings.IndexByte(value[start:], ';')
		if semicolon < 0 {
			break
		}
		semicolon += start
		code, ok := decodeCharacterReference(value[start+2 : semicolon])
		if !ok {
			out.WriteString(value[:start+2])
			value = value[start+2:]
			continue
		}
		out.WriteString(value[:start])
		out.WriteRune(code)
		value = value[semicolon+1:]
	}
	out.WriteString(value)
	return out.String()
}

// decodeCharacterReference decodes the digits of a `&#...;` reference in either
// decimal or `x`-prefixed hex form.
func decodeCharacterReference(digits string) (rune, bool) {
	base := 10
	if len(digits) > 1 && (digits[0] == 'x' || digits[0] == 'X') {
		base = 16
		digits = digits[1:]
	}
	code, err := strconv.ParseInt(digits, base, 32)
	if err != nil || code < 0 {
		return 0, false
	}
	return rune(code), true
}

// unescapedResourceToken finds the next occurrence of token that is not preceded
// by a backslash, the marker the encoder puts on a token occurring in a body.
func unescapedResourceToken(text, token string) int {
	for offset := 0; ; {
		found := strings.Index(text[offset:], token)
		if found < 0 {
			return -1
		}
		found += offset
		if found > 0 && text[found-1] == '\\' {
			offset = found + 1
			continue
		}
		return found
	}
}
