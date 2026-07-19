package tui

import (
	"fmt"
	"os/exec"
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
	addKindMode   bool
	addNameMode   bool
	addEnvMode    bool
	renameMode    bool
	deleteConfirm bool
	deleteID      string
	deleteName    string
	pendingPath   string
	pendingName   string
	pendingKind   model.EngineKind
	nameInput     textinput.Model
	envInput      textinput.Model
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

type enginesTabChangedMsg struct{}

// InModalInput reports whether keys currently belong to a text input, the file
// browser, or a confirmation dialog rather than global shortcuts.
func (m EnginesTabModel) InModalInput() bool {
	return m.addingBrowse || m.addKindMode || m.addNameMode || m.addEnvMode || m.renameMode || m.deleteConfirm
}

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

	if m.addKindMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "l":
				m.pendingKind = model.EngineLlamaCPP
				m.pendingPath, _ = exec.LookPath("llama-server")
			case "v":
				m.pendingKind = model.EngineVLLM
				m.pendingPath, _ = exec.LookPath("vllm")
			case "L":
				m.pendingKind = model.EngineLlamaCPP
			case "V":
				m.pendingKind = model.EngineVLLM
			case "esc":
				m.addKindMode = false
				return m, nil
			default:
				return m, nil
			}
			m.addKindMode = false
			if m.pendingPath != "" {
				m.addNameMode = true
				m.nameInput = textinput.New()
				m.nameInput.Placeholder = "e.g. CUDA build"
				m.nameInput.CharLimit = 80
				m.nameInput.Focus()
				return m, textinput.Blink
			}
			m.addingBrowse = true
			m.fileBrowser = NewFileBrowserModel().SetSelectFile(true).SetSize(m.width, m.height)
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
				if name == "" {
					m.errMsg = styleError.Render("inference engine name is empty")
					return m, nil
				}
				m.pendingName = name
				m.addNameMode = false
				m.addEnvMode = true
				m.envInput = textinput.New()
				m.envInput.Placeholder = "KEY=value; OTHER=value (optional)"
				m.envInput.CharLimit = 1024
				m.envInput.Focus()
				return m, textinput.Blink
			}
		}
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	}

	if m.addEnvMode {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc":
				m.addEnvMode = false
				m.pendingPath, m.pendingName = "", ""
				return m, nil
			case "enter":
				env, err := parseEngineEnvironment(m.envInput.Value())
				if err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				baseArgs := []string(nil)
				if m.pendingKind == model.EngineVLLM {
					baseArgs = []string{"serve"}
				}
				if _, err := m.service.CreateInferenceEngineConfig(m.pendingName, m.pendingPath, m.pendingKind, baseArgs, env); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.engines, _ = m.service.ListInferenceEngines()
				m.cursor = len(m.engines) - 1
				m.addEnvMode = false
				m.pendingPath, m.pendingName = "", ""
				m.errMsg = styleSuccess.Render("✓ inference engine added")
				return m, func() tea.Msg { return enginesTabChangedMsg{} }
			}
		}
		var cmd tea.Cmd
		m.envInput, cmd = m.envInput.Update(msg)
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
			m.addKindMode = true
			m.errMsg = ""
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

func parseEngineEnvironment(value string) (map[string]string, error) {
	env := make(map[string]string)
	for _, assignment := range strings.Split(value, ";") {
		assignment = strings.TrimSpace(assignment)
		if assignment == "" {
			continue
		}
		key, val, ok := strings.Cut(assignment, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t\n") {
			return nil, fmt.Errorf("invalid environment assignment %q", assignment)
		}
		env[key] = strings.TrimSpace(val)
	}
	return env, nil
}

func boxWidthForEngines(w int) int {
	boxW := 68
	if w < 72 {
		boxW = w - 8
	}
	if boxW < 30 {
		boxW = 30
	}
	return boxW
}

func (m EnginesTabModel) View() string {
	t := ActiveTheme
	if m.addingBrowse {
		return m.fileBrowser.View()
	}

	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := styleMuted
	successStyle := lipgloss.NewStyle().Foreground(t.Success)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Inference Engines") + "\n\n")

	if m.deleteConfirm {
		sb.WriteString(styleError.Render("  Delete inference engine?") + "\n\n")
		sb.WriteString(mutedStyle.Render("  Engine: ") + successStyle.Render(m.deleteName) + "\n")
		sb.WriteString(mutedStyle.Render("  This will delete all associated profiles.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  y: confirm  n/esc: cancel") + "\n")
	} else if m.addKindMode {
		sb.WriteString(mutedStyle.Render("  Engine type: ") + "\n\n")
		sb.WriteString("  " + styleKey.Render("l") + mutedStyle.Render(": auto-detect llama.cpp   ") + styleKey.Render("v") + mutedStyle.Render(": auto-detect vLLM") + "\n")
		sb.WriteString("  " + styleKey.Render("L") + mutedStyle.Render(": browse llama.cpp        ") + styleKey.Render("V") + mutedStyle.Render(": browse vLLM") + "\n")
		sb.WriteString(mutedStyle.Render("  esc: cancel") + "\n")
	} else if m.addNameMode {
		sb.WriteString(mutedStyle.Render("  Executable: ") + successStyle.Render(m.pendingPath) + "\n")
		sb.WriteString(mutedStyle.Render("  Name: ") + m.nameInput.View() + "\n\n")
		sb.WriteString(mutedStyle.Render("  enter: continue  esc: cancel") + "\n")
	} else if m.addEnvMode {
		sb.WriteString(mutedStyle.Render("  Environment (optional):") + "\n")
		sb.WriteString("  " + m.envInput.View() + "\n\n")
		sb.WriteString(mutedStyle.Render("  Separate variables with ';'. Values are passed without a shell.") + "\n")
		sb.WriteString(mutedStyle.Render("  enter: create  esc: cancel") + "\n")
	} else if m.renameMode {
		engine, _ := m.selectedEngine()
		sb.WriteString(mutedStyle.Render("  Rename: ") + successStyle.Render(engine.Path) + "\n")
		sb.WriteString("  " + m.nameInput.View() + "\n\n")
		sb.WriteString(mutedStyle.Render("  enter: save  esc: cancel") + "\n")
	} else if len(m.engines) > 0 {
		sb.WriteString(mutedStyle.Render("  Saved inference engines:") + "\n")
		rowW := max(boxWidthForEngines(m.width)-6, 20)
		for i, e := range m.engines {
			if i == m.cursor {
				sb.WriteString("  " + styleSelRow.Render(padToWidth("▶ "+e.Name, rowW-2)) + "\n")
				sb.WriteString("  " + styleSelRowDim.Render(padToWidth("  ["+string(e.Kind)+"] "+e.Path, rowW-2)) + "\n")
			} else {
				sb.WriteString(fmt.Sprintf("    %s\n      %s\n",
					lipgloss.NewStyle().Foreground(t.Text).Render(e.Name), mutedStyle.Render("["+string(e.Kind)+"] "+e.Path)))
			}
		}
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  a: add folder  e: rename  x: delete") + "\n")
	} else {
		sb.WriteString(mutedStyle.Render("  No inference engines configured.") + "\n")
		sb.WriteString(mutedStyle.Render("  Add a llama-server or vLLM executable before creating profiles.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  a: add inference engine") + "\n")
	}

	if m.errMsg != "" {
		sb.WriteString("\n" + m.errMsg)
	}

	boxW := boxWidthForEngines(m.width)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Muted).
			Padding(1, 2).
			Width(boxW).
			MaxHeight(max(m.height, 1)).
			Render(strings.TrimRight(sb.String(), "\n")))
}
