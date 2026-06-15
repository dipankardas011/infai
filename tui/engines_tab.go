package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/model"
)

// EnginesTabModel presents inference engine configuration.
// Persistence is delegated to backend.Service.
type EnginesTabModel struct {
	service *backend.Service
	engines []model.InferenceEngine
	cursor  int

	addingBrowse  bool
	addNameMode   bool
	renameMode    bool
	deleteConfirm bool
	deleteID      string
	deleteName    string
	pendingPath   string
	nameInput     textinput.Model
	fileBrowser   FileBrowserModel

	errMsg string
	width  int
	height int
}

func NewEnginesTabModel(service *backend.Service, w, h int) EnginesTabModel {
	engines, _ := service.ListInferenceEngines()
	return EnginesTabModel{
		service: service,
		engines: engines,
		width:   w,
		height:  h,
	}
}

func (m EnginesTabModel) SetSize(w, h int) EnginesTabModel {
	m.width = w
	m.height = h
	m.fileBrowser = m.fileBrowser.SetSize(w, h)
	return m
}

func (m EnginesTabModel) selectedEngine() (model.InferenceEngine, bool) {
	if len(m.engines) == 0 || m.cursor < 0 || m.cursor >= len(m.engines) {
		return model.InferenceEngine{}, false
	}
	return m.engines[m.cursor], true
}

type enginesTabSavedMsg struct{}
type enginesTabChangedMsg struct{}

func (m EnginesTabModel) Update(msg tea.Msg) (EnginesTabModel, tea.Cmd) {
	if m.deleteConfirm {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				if err := m.service.DeleteInferenceEngine(m.deleteID); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.engines, _ = m.service.ListInferenceEngines()
				if m.cursor >= len(m.engines) {
					m.cursor = max(len(m.engines)-1, 0)
				}
				m.deleteConfirm = false
				m.deleteID = ""
				m.deleteName = ""
				m.errMsg = styleSuccess.Render("✓ inference engine deleted; associated profiles removed")
				return m, func() tea.Msg { return enginesTabChangedMsg{} }
			case "n", "esc":
				m.deleteConfirm = false
				m.deleteID = ""
				m.deleteName = ""
				m.errMsg = ""
				return m, nil
			}
		}
		return m, nil
	}

	if m.addNameMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc":
				m.addNameMode = false
				m.pendingPath = ""
				m.errMsg = ""
				return m, nil
			case "enter":
				name := strings.TrimSpace(m.nameInput.Value())
				if _, err := m.service.CreateInferenceEngine(name, m.pendingPath); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.engines, _ = m.service.ListInferenceEngines()
				m.cursor = len(m.engines) - 1
				m.addNameMode = false
				m.pendingPath = ""
				m.errMsg = styleSuccess.Render("✓ inference engine added")
				return m, func() tea.Msg { return enginesTabChangedMsg{} }
			}
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}

	if m.renameMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc":
				m.renameMode = false
				m.errMsg = ""
				return m, nil
			case "enter":
				engine, ok := m.selectedEngine()
				if !ok {
					m.renameMode = false
					return m, nil
				}
				name := strings.TrimSpace(m.nameInput.Value())
				if err := m.service.UpdateInferenceEngineName(engine.ID, name); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.engines, _ = m.service.ListInferenceEngines()
				if m.cursor >= len(m.engines) {
					m.cursor = max(len(m.engines)-1, 0)
				}
				m.renameMode = false
				m.errMsg = styleSuccess.Render("✓ inference engine renamed")
				return m, func() tea.Msg { return enginesTabChangedMsg{} }
			}
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}

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
			m.pendingPath = absPath
			m.addNameMode = true
			m.nameInput = textinput.New()
			m.nameInput.Placeholder = "e.g. CUDA build"
			m.nameInput.CharLimit = 80
			m.nameInput.Focus()
			m.errMsg = ""
			return m, textinput.Blink
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			m.addingBrowse = true
			m.errMsg = ""
			m.fileBrowser = NewFileBrowserModel().SetSize(m.width, m.height)
			return m, nil
		case "e":
			engine, ok := m.selectedEngine()
			if !ok {
				m.errMsg = styleError.Render("no inference engine selected")
				return m, nil
			}
			m.renameMode = true
			m.nameInput = textinput.New()
			m.nameInput.CharLimit = 80
			m.nameInput.SetValue(engine.Name)
			m.nameInput.Focus()
			m.errMsg = ""
			return m, textinput.Blink
		case "x":
			engine, ok := m.selectedEngine()
			if !ok {
				return m, nil
			}
			m.deleteConfirm = true
			m.deleteID = engine.ID
			m.deleteName = engine.Name
			m.errMsg = ""
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.engines)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m EnginesTabModel) SaveAndExit() (EnginesTabModel, tea.Cmd) {
	return m, func() tea.Msg { return enginesTabSavedMsg{} }
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
	sb.WriteString(titleStyle.Render("Inference Engines") + "\n\n")

	if m.deleteConfirm {
		sb.WriteString(styleError.Render("  Delete inference engine?") + "\n\n")
		sb.WriteString(mutedStyle.Render("  Engine: ") + successStyle.Render(m.deleteName) + "\n")
		sb.WriteString(mutedStyle.Render("  This will delete all associated profiles.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  y: confirm  n/esc: cancel") + "\n")
	} else if m.addNameMode {
		sb.WriteString(mutedStyle.Render("  Folder: ") + successStyle.Render(m.pendingPath) + "\n")
		sb.WriteString(mutedStyle.Render("  Name: ") + m.nameInput.View() + "\n\n")
		sb.WriteString(mutedStyle.Render("  enter: create  esc: cancel") + "\n")
	} else if m.renameMode {
		engine, _ := m.selectedEngine()
		sb.WriteString(mutedStyle.Render("  Rename: ") + successStyle.Render(engine.Path) + "\n")
		sb.WriteString("  " + m.nameInput.View() + "\n\n")
		sb.WriteString(mutedStyle.Render("  enter: save  esc: cancel") + "\n")
	} else if len(m.engines) > 0 {
		sb.WriteString(mutedStyle.Render("  Saved inference engines:") + "\n")
		for i, e := range m.engines {
			prefix := "    "
			nameStyle := lipgloss.NewStyle().Foreground(t.Text)
			if i == m.cursor {
				prefix = selStyle.Render("  ▶ ")
				nameStyle = selStyle
			}
			sb.WriteString(fmt.Sprintf("%s%s\n      %s\n", prefix, nameStyle.Render(e.Name), mutedStyle.Render(e.Path)))
		}
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  a: add folder  e: rename  x: delete") + "\n")
	} else {
		sb.WriteString(mutedStyle.Render("  No inference engines configured.") + "\n")
		sb.WriteString(mutedStyle.Render("  Add a folder containing llama-server before creating or launching profiles.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  a: add inference engine folder") + "\n")
	}

	if m.errMsg != "" {
		sb.WriteString("\n" + m.errMsg)
	}

	boxW := 68
	if m.width < 72 {
		boxW = m.width - 8
	}
	if boxW < 30 {
		boxW = 30
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Muted).
			Padding(1, 2).
			Width(boxW).
			MaxHeight(max(m.height, 1)).
			Render(strings.TrimRight(sb.String(), "\n")))
}
