package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/dipankardas011/infai/config"
)

// RenderHeader returns the top bar showing "infai" + version + theme/help hints.
func RenderHeader(width int) string {
	t := ActiveTheme

	logoStyle := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true)

	versionStyle := lipgloss.NewStyle().
		Foreground(t.Secondary).
		Italic(true)

	hintStyle := lipgloss.NewStyle().
		Foreground(t.Muted)

	left := logoStyle.Render("infai") + " " + versionStyle.Render(config.Version())
	right := hintStyle.Render("t:theme  ?:help")

	if width <= 0 {
		return left
	}

	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)

	padW := width - leftW - rightW
	if padW < 1 {
		return left
	}

	pad := lipgloss.NewStyle().Foreground(t.Muted).Render(
		horizontalLine(padW),
	)
	return left + pad + right
}

// RenderTabs draws a horizontal tab bar.
// tabs is the list of tab names, activeIdx is the selected index.
func RenderTabs(tabs []string, activeIdx, width int) string {
	t := ActiveTheme
	activeStyle := lipgloss.NewStyle().
		Foreground(t.Bg).
		Background(t.Primary).
		Bold(true).
		Padding(0, 2)
	inactiveStyle := lipgloss.NewStyle().
		Foreground(t.Muted).
		Padding(0, 2)
	sepStyle := lipgloss.NewStyle().Foreground(t.Muted)

	var parts []string
	for i, tab := range tabs {
		if i == activeIdx {
			parts = append(parts, activeStyle.Render(tab))
		} else {
			parts = append(parts, inactiveStyle.Render(tab))
		}
	}

	joined := ""
	for i, p := range parts {
		if i > 0 {
			joined += sepStyle.Render("│")
		}
		joined += p
	}

	// Right-fill with muted line
	contentW := lipgloss.Width(joined)
	if width > contentW {
		joined += lipgloss.NewStyle().Foreground(t.Muted).Render(
			horizontalLine(width - contentW),
		)
	}

	return joined
}

func horizontalLine(n int) string {
	if n <= 0 {
		return ""
	}
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = '─'
	}
	return string(runes)
}

// MinWindowSize is the minimum terminal dimensions to display the UI correctly.
const MinWindowWidth = 60
const MinWindowHeight = 20

// RenderMinSizeWarning returns a centered warning when terminal is too small.
func RenderMinSizeWarning(w, h int) string {
	t := ActiveTheme
	warnStyle := lipgloss.NewStyle().
		Foreground(t.Error).
		Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(t.Muted)

	msg := warnStyle.Render("Terminal too small") + "\n\n" +
		hintStyle.Render(fmt.Sprintf("Minimum: %dx%d", MinWindowWidth, MinWindowHeight)) + "\n" +
		hintStyle.Render(fmt.Sprintf("Current: %dx%d", w, h)) + "\n\n" +
		hintStyle.Render("Please resize your terminal window.")

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Error).
			Padding(2, 4).
			Render(msg),
	)
}
