package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
)

func expandPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[2:])
	}
	return filepath.Abs(p)
}

type ExploreModel struct {
	database *db.DB
	dirs     []string

	cursor int
	adding bool
	input  textinput.Model
	errMsg string
	width  int
	height int
}

func NewExploreModel(database *db.DB, dirs []string, w, h int) ExploreModel {
	cp := make([]string, len(dirs))
	copy(cp, dirs)
	ti := textinput.New()
	ti.Placeholder = "/path/to/models"
	ti.CharLimit = 256
	return ExploreModel{database: database, dirs: cp, input: ti, width: w, height: h}
}

func (m ExploreModel) SetSize(w, h int) ExploreModel {
	m.width, m.height = w, h
	return m
}

func (m ExploreModel) Dirs() []string { return m.dirs }

func (m ExploreModel) Update(msg tea.Msg) (ExploreModel, tea.Cmd) {
	if m.adding {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				raw := strings.TrimSpace(m.input.Value())
				m.adding = false
				m.input.SetValue("")
				if raw == "" {
					return m, nil
				}
				path, err := expandPath(raw)
				if err != nil {
					m.errMsg = "bad path: " + err.Error()
					return m, nil
				}
				for _, d := range m.dirs {
					if d == path {
						m.errMsg = "already in list"
						return m, nil
					}
				}
				if err := m.database.AddScanDir(path); err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
				m.dirs = append(m.dirs, path)
				m.cursor = len(m.dirs) - 1
				m.errMsg = ""
				return m, nil
			case "esc":
				m.adding = false
				m.input.SetValue("")
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.dirs)-1 {
				m.cursor++
			}
		case "a":
			m.adding = true
			m.errMsg = ""
			m.input.Focus()
			return m, textinput.Blink
		case "d", "delete":
			if len(m.dirs) == 0 {
				break
			}
			path := m.dirs[m.cursor]
			if err := m.database.RemoveScanDir(path); err != nil {
				m.errMsg = err.Error()
				break
			}
			m.dirs = append(m.dirs[:m.cursor], m.dirs[m.cursor+1:]...)
			if m.cursor >= len(m.dirs) && m.cursor > 0 {
				m.cursor--
			}
			m.errMsg = ""
		}
	}
	return m, nil
}

func (m ExploreModel) View() string {
	t := ActiveTheme
	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Padding(0, 1)
	mutedStyle := lipgloss.NewStyle().Foreground(t.Muted)
	selStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	helpStyle := lipgloss.NewStyle().Foreground(t.Muted).Italic(true)
	errStyle := lipgloss.NewStyle().Foreground(t.Error)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("explore · scan folders") + "\n\n")

	if len(m.dirs) == 0 {
		sb.WriteString(mutedStyle.Render("  no folders — press [a] to add one") + "\n")
	} else {
		for i, d := range m.dirs {
			if i == m.cursor {
				sb.WriteString(selStyle.Render("▶ "+d) + "\n")
			} else {
				sb.WriteString(mutedStyle.Render("  "+d) + "\n")
			}
		}
	}

	sb.WriteString("\n")
	if m.adding {
		sb.WriteString(lipgloss.NewStyle().Foreground(t.Secondary).Render("add folder: "))
		sb.WriteString(m.input.View() + "\n")
		sb.WriteString(helpStyle.Render("enter: confirm  esc: cancel add") + "\n")
	} else {
		if m.errMsg != "" {
			sb.WriteString(errStyle.Render(m.errMsg) + "\n")
		}
		sb.WriteString(helpStyle.Render("a: add  d: remove  ↑↓: navigate  esc: back & rescan"))
	}
	content := sb.String()
	tBox := ActiveTheme

	boxWidth := 60
	if m.width < 60 {
		boxWidth = m.width - 4
	}
	if boxWidth < 0 {
		boxWidth = 0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tBox.Primary).
		Padding(1, 2).
		Width(boxWidth)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}
