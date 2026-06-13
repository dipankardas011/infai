package tui

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
)

// EnginesTabModel manages inference engine executor configuration.
type EnginesTabModel struct {
	database  *db.DB
	executors []db.Executor
	cursor    int
	detected  string
	path      string // current effective path

	// Add file browser for picking new executor
	addingBrowse bool
	fileBrowser  FileBrowserModel

	errMsg string
	width  int
	height int
}

func NewEnginesTabModel(database *db.DB, currentPath string, w, h int) EnginesTabModel {
	executors, _ := database.ListExecutors()

	detected := ""
	if p, err := exec.LookPath("llama-server"); err == nil {
		detected = p
	}

	// Determine effective path
	path := currentPath
	if path == "" && detected != "" {
		path = detected
	}

	// Find cursor on default
	curIdx := 0
	for i, e := range executors {
		if e.IsDefault {
			curIdx = i
			break
		}
	}

	return EnginesTabModel{
		database:  database,
		executors: executors,
		cursor:    curIdx,
		detected:  detected,
		path:      path,
		width:     w,
		height:    h,
	}
}

func (m EnginesTabModel) SetSize(w, h int) EnginesTabModel {
	m.width = w
	m.height = h
	m.fileBrowser = m.fileBrowser.SetSize(w, h)
	return m
}

func (m EnginesTabModel) EffectivePath() string {
	return m.path
}

type enginesTabSavedMsg struct{ Path string }

func (m EnginesTabModel) Update(msg tea.Msg) (EnginesTabModel, tea.Cmd) {
	if m.addingBrowse {
		var cmd tea.Cmd
		m.fileBrowser, cmd = m.fileBrowser.Update(msg)
		if _, ok := msg.(tea.KeyMsg); ok {
			switch msg.(type) {
			case FileBrowserSavedMsg:
			default:
				return m, cmd
			}
		}
		if fm, ok := msg.(FileBrowserSavedMsg); ok {
			m.addingBrowse = false
			if fm.Path == "" {
				return m, nil
			}

			absPath, err := expandPath(fm.Path)
			if err != nil {
				m.errMsg = styleError.Render("bad path: " + err.Error())
				return m, nil
			}

			isDefault := len(m.executors) == 0
			err = m.database.UpsertExecutor(db.Executor{
				ID:        "llamacpp",
				Path:      absPath,
				IsDefault: isDefault,
			})
			if err != nil {
				m.errMsg = styleError.Render(err.Error())
				return m, nil
			}

			m.executors, _ = m.database.ListExecutors()
			m.path = absPath
			m.errMsg = styleSuccess.Render("✓ executor configured")
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			m.addingBrowse = true
			m.errMsg = ""
			m.fileBrowser = NewFileBrowserModel().SetSize(m.width, m.height).SetSelectFile(true)
			return m, nil
		case "enter":
			if len(m.executors) > 0 && m.cursor < len(m.executors) {
				id := m.executors[m.cursor].ID
				_ = m.database.SetDefaultExecutor(id)
				m.executors, _ = m.database.ListExecutors()
				m.path = m.executors[m.cursor].Path
				m.errMsg = styleSuccess.Render("✓ default set")
			}
		case "d":
			if m.detected != "" {
				_ = m.database.UpsertExecutor(db.Executor{
					ID:        "llamacpp",
					Path:      m.detected,
					IsDefault: true,
				})
				m.executors, _ = m.database.ListExecutors()
				m.path = m.detected
				m.errMsg = styleSuccess.Render("✓ using system llama-server")
			} else {
				m.errMsg = styleError.Render("llama-server not found in PATH")
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.executors)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m EnginesTabModel) SaveAndExit() (EnginesTabModel, tea.Cmd) {
	return m, func() tea.Msg { return enginesTabSavedMsg{Path: m.path} }
}

func (m EnginesTabModel) View() string {
	t := ActiveTheme

	if m.addingBrowse {
		return m.fileBrowser.View()
	}

	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := styleMuted
	successStyle := lipgloss.NewStyle().Foreground(t.Success)
	selStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Inference Engine") + "\n\n")

	// Show current path
	sb.WriteString(mutedStyle.Render("  Executor: ") + successStyle.Render(m.path) + "\n")

	if m.detected != "" && m.path != m.detected {
		sb.WriteString(mutedStyle.Render("  Detected: ") + mutedStyle.Render(m.detected) + "\n")
	} else if m.detected == "" && m.path == "" {
		sb.WriteString(mutedStyle.Render("  Not found in PATH") + "\n")
	}

	sb.WriteString("\n")

	if len(m.executors) > 0 {
		sb.WriteString(mutedStyle.Render("  Saved executors:") + "\n")
		for i, e := range m.executors {
			prefix := "    "
			style := mutedStyle
			def := ""
			if e.IsDefault {
				def = successStyle.Render(" (default)")
			}
			if i == m.cursor {
				prefix = selStyle.Render("  ▶ ")
				style = selStyle
			}
			sb.WriteString(fmt.Sprintf("%s%s: %s%s\n", prefix, style.Render(e.ID), e.Path, def))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(mutedStyle.Render("  a: add  d: auto-detect  enter: set default") + "\n")

	if m.errMsg != "" {
		sb.WriteString("\n" + m.errMsg)
	}

	content := sb.String()
	boxW := 60
	if m.width < 64 {
		boxW = m.width - 8
	}
	if boxW < 30 {
		boxW = 30
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(boxW)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		boxStyle.Render(strings.TrimRight(content, "\n")))
}
