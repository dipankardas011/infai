package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ThemeSelectorModel struct {
	cursor int
	// original is the theme active when the selector opened, so esc can
	// revert the live preview.
	original string
	width    int
	height   int
}

func NewThemeSelectorModel(w, h int) ThemeSelectorModel {
	// Find current theme index
	curIdx := 0
	for i, t := range ThemeList {
		if t.Name == ActiveTheme.Name {
			curIdx = i
			break
		}
	}
	return ThemeSelectorModel{
		cursor:   curIdx,
		original: ActiveTheme.Name,
		width:    w,
		height:   h,
	}
}

func (m ThemeSelectorModel) SetSize(w, h int) ThemeSelectorModel {
	m.width, m.height = w, h
	return m
}

func (m ThemeSelectorModel) Update(msg tea.Msg) (ThemeSelectorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			} else {
				m.cursor = len(ThemeList) - 1
			}
		case "down", "j":
			if m.cursor < len(ThemeList)-1 {
				m.cursor++
			} else {
				m.cursor = 0
			}
		}
		// Live preview: the whole UI re-renders in the highlighted theme.
		SetTheme(ThemeList[m.cursor].Name)
	}
	return m, nil
}

// Revert restores the theme that was active when the selector opened.
func (m ThemeSelectorModel) Revert() {
	SetTheme(m.original)
}

func (m ThemeSelectorModel) SelectedTheme() Theme {
	return ThemeList[m.cursor]
}

// swatch renders sample dots in a theme's own colors so every row shows its
// palette regardless of the active theme.
func themeSwatch(t Theme) string {
	dot := func(c lipgloss.Color) string {
		return lipgloss.NewStyle().Foreground(c).Render("●")
	}
	return dot(t.Primary) + " " + dot(t.Secondary) + " " + dot(t.Success) + " " + dot(t.Error)
}

func (m ThemeSelectorModel) View() string {
	t := ActiveTheme
	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Padding(0, 1)
	selStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(t.Text)
	mutedStyle := lipgloss.NewStyle().Foreground(t.Muted)

	// Keep the cursor visible when the list is taller than the window.
	maxVisible := max(m.height-8, 3)
	scrollOff := 0
	if m.cursor >= maxVisible {
		scrollOff = m.cursor - maxVisible + 1
	}
	end := min(scrollOff+maxVisible, len(ThemeList))

	nameW := 0
	for _, theme := range ThemeList {
		nameW = max(nameW, len(theme.Name))
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("select theme") + "\n\n")

	for i := scrollOff; i < end; i++ {
		theme := ThemeList[i]
		name := theme.Name + strings.Repeat(" ", nameW-len(theme.Name))
		if i == m.cursor {
			sb.WriteString(selStyle.Render("▶ "+name) + "  " + themeSwatch(theme) + "\n")
		} else {
			sb.WriteString(textStyle.Render("  "+name) + "  " + themeSwatch(theme) + "\n")
		}
	}
	if len(ThemeList) > maxVisible {
		sb.WriteString(mutedStyle.Render("  ↑/↓ for more") + "\n")
	}

	sb.WriteString("\n" + mutedStyle.Render("live preview — enter: apply  esc: revert"))

	content := sb.String()

	boxW := 44
	if m.width < 48 {
		boxW = m.width - 4
	}
	if boxW < 0 {
		boxW = 0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(1, 2).
		Width(boxW).
		MaxHeight(max(m.height, 1))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}
