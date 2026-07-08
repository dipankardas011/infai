package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type FileBrowserSavedMsg struct{ Path string }

type fileEntry struct {
	name  string
	path  string
	isDir bool
}

type FileBrowserModel struct {
	cursor      int
	entries     []fileEntry
	currentDir  string
	width       int
	height      int
	selectFile  bool
	filtering   bool
	filterInput textinput.Model
}

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

func truncatePath(path string, maxLen int) string {
	if maxLen < 4 {
		return path
	}
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-(maxLen-3):]
}

func NewFileBrowserModel() FileBrowserModel {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	if cwd == "" {
		cwd = home
	}
	entries := loadDirEntries(cwd)

	ti := textinput.New()
	ti.Placeholder = "Filter..."
	ti.Prompt = "/"
	ti.CharLimit = 128

	return FileBrowserModel{
		cursor:      0,
		entries:     entries,
		currentDir:  cwd,
		width:       60,
		height:      20,
		filterInput: ti,
	}
}

func (m FileBrowserModel) filteredEntries() []fileEntry {
	if !m.filtering && m.filterInput.Value() == "" {
		return m.entries
	}
	term := strings.ToLower(m.filterInput.Value())
	if term == "" {
		return m.entries
	}
	var filtered []fileEntry
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.name), term) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func loadDirEntries(dir string) []fileEntry {
	entries := []fileEntry{}
	files, err := os.ReadDir(dir)
	if err != nil {
		return entries
	}
	for _, f := range files {
		name := f.Name()
		if name == "." || name == ".." {
			continue
		}
		if name[0] == '.' {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{
			name:  name,
			path:  filepath.Join(dir, name),
			isDir: info.IsDir(),
		})
	}
	slices.SortFunc(entries, func(a, b fileEntry) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	return entries
}

func (m FileBrowserModel) SetSize(w, h int) FileBrowserModel {
	m.width, m.height = w, h
	return m
}

func (m FileBrowserModel) SetSelectFile(b bool) FileBrowserModel {
	m.selectFile = b
	return m
}

func (m FileBrowserModel) Update(msg tea.Msg) (FileBrowserModel, tea.Cmd) {
	if m.filtering {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc", "/":
				m.filtering = false
				m.filterInput.SetValue("")
				m.filterInput.Blur()
				m.cursor = 0
				return m, nil
			case "enter":
				m.filtering = false
				m.filterInput.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)

		filtered := m.filteredEntries()
		if m.cursor >= len(filtered) && len(filtered) > 0 {
			m.cursor = len(filtered) - 1
		} else if len(filtered) == 0 {
			m.cursor = 0
		}
		return m, cmd
	}

	filtered := m.filteredEntries()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case "enter":
			if len(filtered) == 0 {
				if !m.selectFile {
					var nm FileBrowserModel = m
					return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: m.currentDir} }
				}
				return m, nil
			}
			entry := filtered[m.cursor]
			if m.selectFile {
				if !entry.isDir {
					var nm FileBrowserModel = m
					return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: entry.path} }
				}
			} else {
				if entry.isDir {
					var nm FileBrowserModel = m
					return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: entry.path} }
				}
			}
		case "right":
			if len(filtered) > 0 && filtered[m.cursor].isDir {
				m.currentDir = filtered[m.cursor].path
				m.entries = loadDirEntries(m.currentDir)
				m.filterInput.SetValue("")
				m.cursor = 0
			}
		case "/":
			m.filtering = true
			m.filterInput.Focus()
			return m, textinput.Blink
		case "left", "backspace", "b":
			if m.currentDir != "/" {
				m.currentDir = filepath.Dir(m.currentDir)
				if m.currentDir == "." {
					m.currentDir = "/"
				}
				m.entries = loadDirEntries(m.currentDir)
				m.filterInput.SetValue("")
				m.cursor = 0
			}
		case "home":
			home, _ := os.UserHomeDir()
			m.currentDir = home
			m.entries = loadDirEntries(home)
			m.filterInput.SetValue("")
			m.cursor = 0
		case "esc":
			if m.filterInput.Value() != "" {
				m.filterInput.SetValue("")
				m.cursor = 0
				return m, nil
			}
			var nm FileBrowserModel = m
			return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: ""} }
		}
	}
	return m, nil
}

func (m FileBrowserModel) View() string {
	t := ActiveTheme
	mutedStyle := lipgloss.NewStyle().Foreground(t.Muted)
	dirStyle := lipgloss.NewStyle().Foreground(t.Secondary).Bold(true)
	selStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	fileIcon := "  "
	folderIcon := lipgloss.NewStyle().Foreground(t.Primary).Render("> ")

	boxWidth := max(m.width-4, 0)

	var sb strings.Builder
	sb.WriteString(dirStyle.Render(truncatePath(m.currentDir, boxWidth)) + "\n")

	if m.filtering || m.filterInput.Value() != "" {
		sb.WriteString(m.filterInput.View() + "\n")
	}

	maxEntries := m.height - 6
	if m.filtering || m.filterInput.Value() != "" {
		maxEntries--
	}
	if maxEntries < 0 {
		maxEntries = 0
	}

	filtered := m.filteredEntries()

	if len(filtered) == 0 {
		sb.WriteString(mutedStyle.Render("  (empty)") + "\n")
	} else {
		start := 0
		end := len(filtered)
		if end > maxEntries {
			start = max(m.cursor-maxEntries/2, 0)
			end = start + maxEntries
			if end > len(filtered) {
				end = len(filtered)
				start = max(end-maxEntries, 0)
			}
		}

		maxNameLen := max(boxWidth-6, 5)

		for i := start; i < end; i++ {
			entry := filtered[i]
			icon := fileIcon
			if entry.isDir {
				icon = folderIcon
			}

			dispName := entry.name
			if len(dispName) > maxNameLen {
				dispName = dispName[:maxNameLen-3] + "..."
			}

			if i == m.cursor {
				sb.WriteString(selStyle.Render("▶ "+icon+dispName) + "\n")
			} else {
				sb.WriteString(mutedStyle.Render("  "+icon+dispName) + "\n")
			}
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 1).
		Width(boxWidth).
		MaxHeight(max(m.height, 1))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(strings.TrimRight(sb.String(), "\n")))
}
