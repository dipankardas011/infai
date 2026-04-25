package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ThemeSelectorModel struct {
	cursor int
	width  int
	height int
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
		cursor: curIdx,
		width:  w,
		height: h,
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
	}
	return m, nil
}

func (m ThemeSelectorModel) SelectedTheme() Theme {
	return ThemeList[m.cursor]
}

func (m ThemeSelectorModel) View() string {
	t := ActiveTheme
	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Padding(0, 1)
	selStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(t.Muted)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("select theme") + "\n\n")

	for i, theme := range ThemeList {
		if i == m.cursor {
			sb.WriteString(selStyle.Render("▶ "+theme.Name) + "\n")
		} else {
			sb.WriteString("  " + theme.Name + "\n")
		}
	}

	sb.WriteString("\n" + mutedStyle.Render("enter: select  esc: cancel"))

	content := sb.String()

	boxW := 40
	if m.width < 40 {
		boxW = m.width - 4
	}
	if boxW < 0 {
		boxW = 0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(boxW)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}
