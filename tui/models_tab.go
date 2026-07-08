package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
)

// ModelsTabModel is presentation for scan directory management.
// DB/scanner work is delegated to backend.Service.
type ModelsTabModel struct {
	service   *backend.Service
	dirs      []string
	modelCnt  int
	cursor    int
	scrollOff int

	addingBrowse  bool
	fileBrowser   FileBrowserModel
	removeConfirm bool

	syncing bool
	spinner spinner.Model

	errMsg string
	width  int
	height int
}

type modelsTabSyncDoneMsg struct {
	removed, updated int
	err              error
}

type modelsTabChangedMsg struct{}

func NewModelsTabModel(service *backend.Service, dirs []string, w, h int) ModelsTabModel {
	models, _ := service.ListModels()
	cp := make([]string, len(dirs))
	copy(cp, dirs)

	s := spinner.New()
	s.Spinner = spinner.Dot

	return ModelsTabModel{
		service:  service,
		dirs:     cp,
		modelCnt: len(models),
		spinner:  s,
		width:    w,
		height:   h,
	}
}

func (m ModelsTabModel) SetSize(w, h int) ModelsTabModel {
	m.width = w
	m.height = h
	m.fileBrowser = m.fileBrowser.SetSize(w, h)
	return m
}

func (m *ModelsTabModel) Close() {}

// InModalInput reports whether keys currently belong to the file browser or a
// confirmation dialog rather than global shortcuts.
func (m ModelsTabModel) InModalInput() bool {
	return m.addingBrowse || m.removeConfirm
}

func (m ModelsTabModel) Update(msg tea.Msg) (ModelsTabModel, tea.Cmd) {
	if m.removeConfirm {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				m.removeConfirm = false
				if len(m.dirs) == 0 || m.cursor >= len(m.dirs) {
					return m, nil
				}
				path := m.dirs[m.cursor]
				if err := m.service.RemoveScanDir(path); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.dirs = append(m.dirs[:m.cursor], m.dirs[m.cursor+1:]...)
				if m.cursor >= len(m.dirs) && m.cursor > 0 {
					m.cursor--
				}
				models, _ := m.service.ListModels()
				m.modelCnt = len(models)
				m.errMsg = styleSuccess.Render("✓ removed")
				return m, func() tea.Msg { return modelsTabChangedMsg{} }
			case "n", "esc":
				m.removeConfirm = false
				m.errMsg = ""
				return m, nil
			}
		}
		return m, nil
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
			for _, d := range m.dirs {
				if d == fm.Path {
					m.errMsg = styleError.Render("already in list")
					return m, nil
				}
			}
			if err := m.service.AddScanDir(fm.Path); err != nil {
				m.errMsg = styleError.Render(err.Error())
				return m, nil
			}
			m.dirs = append(m.dirs, fm.Path)
			m.cursor = len(m.dirs) - 1
			m.errMsg = styleSuccess.Render("✓ added " + filepath.Base(fm.Path))
		}
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
			m.addingBrowse = true
			m.errMsg = ""
			home, _ := os.UserHomeDir()
			m.fileBrowser = NewFileBrowserModel()
			m.fileBrowser.currentDir = home
			m.fileBrowser.entries = loadDirEntries(home)
			m.fileBrowser = m.fileBrowser.SetSize(m.width, m.height)
			return m, nil
		case "d", "x", "delete":
			if len(m.dirs) == 0 || m.cursor >= len(m.dirs) {
				break
			}
			m.removeConfirm = true
			m.errMsg = ""
			return m, nil
		case "s":
			if m.syncing || len(m.dirs) == 0 {
				break
			}
			folders := append([]string(nil), m.dirs...)
			service := m.service
			m.syncing = true
			m.errMsg = ""
			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					res, err := service.SyncModels(folders)
					return modelsTabSyncDoneMsg{removed: res.Removed, updated: res.Updated, err: err}
				},
			)
		}
	case modelsTabSyncDoneMsg:
		m.syncing = false
		if msg.err != nil {
			m.errMsg = styleError.Render(msg.err.Error())
		} else {
			m.errMsg = styleSuccess.Render(fmt.Sprintf("✓ synced: %d updated, %d removed", msg.updated, msg.removed))
			models, _ := m.service.ListModels()
			m.modelCnt = len(models)
		}
	case spinner.TickMsg:
		if m.syncing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	maxVisible := m.height - 8
	if maxVisible < 1 {
		maxVisible = 1
	}
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	} else if m.cursor >= m.scrollOff+maxVisible {
		m.scrollOff = m.cursor - maxVisible + 1
	}
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}

	return m, nil
}

func (m ModelsTabModel) View() string {
	t := ActiveTheme
	if m.addingBrowse {
		return m.fileBrowser.View()
	}

	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := styleMuted
	successStyle := lipgloss.NewStyle().Foreground(t.Success)

	var sb strings.Builder

	if m.removeConfirm && m.cursor < len(m.dirs) {
		sb.WriteString(titleStyle.Render("Model Directories") + "\n\n")
		sb.WriteString(styleError.Render("  Remove this scan folder?") + "\n\n")
		sb.WriteString(mutedStyle.Render("  Folder: ") + successStyle.Render(m.dirs[m.cursor]) + "\n")
		sb.WriteString(mutedStyle.Render("  Models under it disappear from the list after the next sync.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  y: confirm  n/esc: cancel") + "\n")
		boxW := max(m.width-4, 30)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Error).
			Padding(1, 2).
			Width(boxW).
			MaxHeight(max(m.height, 1)).
			Render(sb.String())
	}

	sb.WriteString(titleStyle.Render("Model Directories") + "\n")
	if m.modelCnt > 0 {
		sb.WriteString(mutedStyle.Render(fmt.Sprintf("  %d models discovered\n", m.modelCnt)))
	}
	sb.WriteString("\n")

	maxVisible := max(m.height-10, 3)
	if len(m.dirs) == 0 {
		sb.WriteString(mutedStyle.Render("  No folders configured.") + "\n")
		sb.WriteString(mutedStyle.Render("  Press [a] to add a scan folder.") + "\n")
	} else {
		end := m.scrollOff + maxVisible
		if end > len(m.dirs) {
			end = len(m.dirs)
		}
		for i := m.scrollOff; i < end; i++ {
			d := m.dirs[i]
			availW := max(m.width-10, 10)
			display := d
			if len(display) > availW {
				display = "…" + display[len(display)-(availW-3):]
			}
			if i == m.cursor {
				sb.WriteString(styleSelRow.Render(padToWidth("▶ "+display, availW+2)) + "\n")
			} else {
				sb.WriteString("  " + mutedStyle.Render(display) + "\n")
			}
		}
	}

	sb.WriteString("\n")
	if m.syncing {
		sb.WriteString(styleSelected.Render(m.spinner.View()+" syncing...") + "\n")
	} else if m.errMsg != "" {
		sb.WriteString(m.errMsg + "\n")
	}

	boxW := max(m.width-4, 30)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(boxW).
		MaxHeight(max(m.height, 1)).
		Render(sb.String())
}
