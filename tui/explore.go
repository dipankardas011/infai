package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/scanner"
)

type FileBrowserSavedMsg struct{ Path string }

type fileEntry struct {
	name  string
	path  string
	isDir bool
}

type FileBrowserModel struct {
	cursor     int
	entries    []fileEntry
	currentDir string
	width      int
	height     int
}

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

func NewFileBrowserModel() FileBrowserModel {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	if cwd == "" {
		cwd = home
	}
	entries := loadDirEntries(cwd)
	return FileBrowserModel{
		cursor:     0,
		entries:    entries,
		currentDir: cwd,
		width:      60,
		height:     20,
	}
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

func (m FileBrowserModel) Update(msg tea.Msg) (FileBrowserModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.entries) == 0 {
				var nm FileBrowserModel = m
				return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: m.currentDir} }
			}
			entry := m.entries[m.cursor]
			if entry.isDir {
				var nm FileBrowserModel = m
				return nm, func() tea.Msg { return FileBrowserSavedMsg{Path: entry.path} }
			}
		case "right", "/":
			if len(m.entries) > 0 && m.entries[m.cursor].isDir {
				m.currentDir = m.entries[m.cursor].path
				m.entries = loadDirEntries(m.currentDir)
				m.cursor = 0
			}
		case "left", "backspace", "b":
			if m.currentDir != "/" {
				m.currentDir = filepath.Dir(m.currentDir)
				if m.currentDir == "." {
					m.currentDir = "/"
				}
				m.entries = loadDirEntries(m.currentDir)
				m.cursor = 0
			}
		case "home":
			home, _ := os.UserHomeDir()
			m.currentDir = home
			m.entries = loadDirEntries(home)
			m.cursor = 0
		case "esc":
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

	var sb strings.Builder
	sb.WriteString(dirStyle.Render(m.currentDir) + "\n")

	maxEntries := m.height - 4
	if maxEntries < 0 {
		maxEntries = 0
	}

	displayEntries := m.entries
	if len(displayEntries) > maxEntries {
		displayEntries = displayEntries[:maxEntries]
	}

	if len(displayEntries) == 0 {
		sb.WriteString(mutedStyle.Render("  (empty)") + "\n")
	} else {
		start := 0
		end := len(displayEntries)
		if m.cursor >= maxEntries/2 && len(m.entries) > maxEntries {
			start = m.cursor - maxEntries/2
			end = start + maxEntries
			if end > len(m.entries) {
				end = len(m.entries)
				start = end - maxEntries
			}
		}
		for i := start; i < end; i++ {
			entry := displayEntries[i]
			icon := fileIcon
			if entry.isDir {
				icon = folderIcon
			}
			if i == m.cursor {
				sb.WriteString(selStyle.Render("▶ "+icon+entry.name) + "\n")
			} else {
				sb.WriteString(mutedStyle.Render("  "+icon+entry.name) + "\n")
			}
		}
	}

	boxWidth := m.width - 4
	if boxWidth < 0 {
		boxWidth = 0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 1).
		Width(boxWidth)

	return boxStyle.Render(sb.String())
}

type ExploreModel struct {
	database *db.DB
	dirs     []string
	syncChan chan syncRequest

	cursor       int
	addingBrowse bool
	fileBrowser  FileBrowserModel
	errMsg       string
	width        int
	height       int
	syncing      bool
	spinner      spinner.Model
}

func NewExploreModel(database *db.DB, dirs []string, w, h int) ExploreModel {
	cp := make([]string, len(dirs))
	copy(cp, dirs)
	s := spinner.New()
	s.Spinner = spinner.Dot
	m := ExploreModel{
		database: database,
		dirs:     cp,
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

func (m ExploreModel) AddingBrowse() bool { return m.addingBrowse }

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
			path := fm.Path
			if slices.Contains(m.dirs, path) {
				m.errMsg = "already in list"
				return m, nil
			}
			if err := m.database.AddScanDir(path); err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
			m.dirs = append(m.dirs, path)
			m.cursor = len(m.dirs) - 1
			m.errMsg = styleSuccess.Render("✓ added " + filepath.Base(path))
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
			m.fileBrowser = NewFileBrowserModel().SetSize(m.width, m.height)
			return m, nil
		case "d", "delete":
			if len(m.dirs) == 0 {
				break
			}
			path := m.dirs[m.cursor]
			if err := m.database.RemoveScanDir(path); err != nil {
				m.errMsg = err.Error()
				break
			}
			m.dirs = slices.Delete(m.dirs, m.cursor, m.cursor+1)
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
	} else if m.addingBrowse {
		return m.fileBrowser.View()
	} else {
		if m.errMsg != "" {
			if strings.HasPrefix(m.errMsg, "\x1b[") || strings.HasPrefix(m.errMsg, "✓") {
				sb.WriteString(m.errMsg + "\n")
			} else {
				sb.WriteString(errStyle.Render(m.errMsg) + "\n")
			}
		}
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
