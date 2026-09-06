package tui

import (
	"regexp"
	"strings"
)

var (
	inlineMath = regexp.MustCompile(`\$([^$\n]+)\$`)
	latexText  = regexp.MustCompile(`\\(?:text|mathrm|mathbf|operatorname)\{([^{}]*)\}`)
	latexFrac  = regexp.MustCompile(`\\frac\{([^{}]*)\}\{([^{}]*)\}`)
	latexCmd   = regexp.MustCompile(`\\([A-Za-z]+)`)
)

// normalizeMarkdownMath keeps common model-generated LaTeX readable in a
// terminal. GFM has no math primitive, so leaving these spans untouched would
// expose commands such as \text{} and \times to the user.
func normalizeMarkdownMath(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	var display []string
	inFence, inDisplay := false, false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == "$$" {
			if inDisplay {
				out = append(out, readableMath(strings.Join(display, " ")))
				display = nil
			} else {
				display = nil
			}
			inDisplay = !inDisplay
			continue
		}
		if inDisplay {
			display = append(display, strings.TrimSpace(line))
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 4 && strings.HasPrefix(trimmed, "$$") && strings.HasSuffix(trimmed, "$$") {
			out = append(out, readableMath(strings.TrimSpace(trimmed[2:len(trimmed)-2])))
			continue
		}
		out = append(out, normalizeInlineMath(line))
	}
	if inDisplay {
		out = append(out, "$$")
		out = append(out, display...)
	}
	return strings.Join(out, "\n")
}

func normalizeInlineMath(line string) string {
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = inlineMath.ReplaceAllStringFunc(parts[i], func(value string) string {
			expression := strings.TrimSuffix(strings.TrimPrefix(value, "$"), "$")
			if expression == "" {
				return value
			}
			if !strings.Contains(expression, " ") || strings.ContainsAny(expression, `\_^{}{}`) {
				return readableMath(expression)
			}
			return value
		})
	}
	return strings.Join(parts, "`")
}

func readableMath(value string) string {
	for {
		next := latexText.ReplaceAllString(value, "$1")
		next = latexFrac.ReplaceAllString(next, "($1)/($2)")
		if next == value {
			break
		}
		value = next
	}
	value = strings.NewReplacer(
		`\times`, "×",
		`\cdot`, "·",
		`\leq`, "≤",
		`\geq`, "≥",
		`\neq`, "≠",
		`\rightarrow`, "→",
		`\left`, "",
		`\right`, "",
		`\_`, "_",
	).Replace(value)
	value = latexCmd.ReplaceAllString(value, "$1")
	value = strings.NewReplacer("{", "", "}", "").Replace(value)
	return value
}
