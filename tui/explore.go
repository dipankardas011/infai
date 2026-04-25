package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/scanner"
)

type syncRequest struct {
	folders []string
	result  chan syncResult
}

type syncResult struct {
	removed int
	updated int
	err     error
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

type ExploreModel struct {
	database *db.DB
	dirs     []string
	syncChan chan syncRequest

	cursor  int
	adding  bool
	input   textinput.Model
	errMsg  string
	width   int
	height  int
	syncing bool
	spinner spinner.Model
}

func NewExploreModel(database *db.DB, dirs []string, w, h int) ExploreModel {
	cp := make([]string, len(dirs))
	copy(cp, dirs)
	ti := textinput.New()
	ti.Placeholder = "/path/to/models"
	ti.CharLimit = 256
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := ExploreModel{
		database: database,
		dirs:     cp,
		input:    ti,
		width:    w,
		height:   h,
		spinner:  s,
		syncChan: make(chan syncRequest),
	}
	go m.syncWorker()
	return m
}

func (m ExploreModel) SetSize(w, h int) ExploreModel {
	m.width, m.height = w, h
	return m
}

func (m ExploreModel) Dirs() []string { return m.dirs }

func (m *ExploreModel) Close() {
	if m.syncChan != nil {
		close(m.syncChan)
		m.syncChan = nil
	}
}

func (m ExploreModel) syncWorker() {
	for req := range m.syncChan {
		entries, err := scanner.Scan(req.folders)
		if err != nil {
			req.result <- syncResult{err: fmt.Errorf("scan: %v", err)}
			continue
		}
		var metaErr error
		for i := range entries {
			if err := scanner.LoadModelMetadata(m.database, &entries[i]); err != nil {
				metaErr = fmt.Errorf("load metadata: %v", err)
				break
			}
		}
		if metaErr != nil {
			req.result <- syncResult{err: metaErr}
			continue
		}
		removed, updated, err := m.database.Sync(entries)
		if err != nil {
			req.result <- syncResult{err: fmt.Errorf("sync: %v", err)}
			continue
		}
		req.result <- syncResult{removed: removed, updated: updated}
	}
}

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
		case "s":
			if m.syncing || len(m.dirs) == 0 {
				break
			}
			folders := make([]string, len(m.dirs))
			copy(folders, m.dirs)
			result := make(chan syncResult, 1)
			ch := m.syncChan
			m.syncing = true
			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					ch <- syncRequest{folders: folders, result: result}
					res := <-result
					return syncDoneMsg{removed: res.removed, updated: res.updated, err: res.err}
				},
			)
		}
	case syncDoneMsg:
		m.syncing = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		} else if msg.removed == 0 && msg.updated == 0 {
			m.errMsg = styleSuccess.Render("✓ sync done")
		} else {
			m.errMsg = fmt.Sprintf("synced: %d updated, %d removed", msg.updated, msg.removed)
		}
	case spinner.TickMsg:
		if m.syncing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
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
	if m.syncing {
		sb.WriteString(styleSelected.Render(m.spinner.View()+" syncing...") + "\n")
	} else if m.adding {
		sb.WriteString(lipgloss.NewStyle().Foreground(t.Secondary).Render("add folder: "))
		sb.WriteString(m.input.View() + "\n")
		sb.WriteString(helpStyle.Render("enter: confirm  esc: cancel add") + "\n")
	} else {
		if m.errMsg != "" {
			sb.WriteString(errStyle.Render(m.errMsg) + "\n")
		}
		sb.WriteString(helpStyle.Render("a: add  d: remove  s: sync all  ↑↓: navigate  esc: back"))
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
