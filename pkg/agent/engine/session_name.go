package engine

import "strings"

func sessionNameFromPrompt(prompt string) string {
	const maxRunes = 56
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" {
		return "Untitled session"
	}
	runes := []rune(title)
	if len(runes) <= maxRunes {
		return title
	}
	cut := maxRunes
	for i := maxRunes; i > maxRunes/2; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}
