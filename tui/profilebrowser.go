package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
)

// ProfileBrowserModel shows every saved profile across every discovered model.
// It intentionally mirrors ModelListModel's bubbles/list implementation.
type ProfileBrowserModel struct {
	list          list.Model
	entries       []db.ProfileEntry
	deleteConfirm bool
	deleteID      int64
	statusMsg     string
	width         int
	height        int
	initialized   bool
}

type profileBrowserItem struct {
	entry db.ProfileEntry
}

func (p profileBrowserItem) FilterValue() string {
	parts := []string{
		p.entry.Profile.Name,
		p.entry.Model.DisplayName,
		p.entry.Model.Type,
		p.entry.Model.ScanDir,
		p.entry.Model.GGUFPath,
	}
	return strings.Join(parts, " ")
}

func (p profileBrowserItem) Title() string {
	return p.entry.Profile.Name
}

func (p profileBrowserItem) Description() string {
	profile := p.entry.Profile
	return strings.TrimSpace(fmt.Sprintf("ctx:%s port:%d host:%s %s", fmtInt(profile.ContextSize), profile.Port, profile.Host, profileLocation(p.entry)))
}

type profileBrowserDelegate struct{}

func (d profileBrowserDelegate) Height() int                             { return 3 }
func (d profileBrowserDelegate) Spacing() int                            { return 1 }
func (d profileBrowserDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d profileBrowserDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(profileBrowserItem)
	if !ok {
		return
	}
	selected := index == m.Index()

	p := i.entry.Profile
	title := lipgloss.NewStyle().Foreground(colorText).Render("  " + p.Name)
	if selected {
		title = styleSelected.Render("▶ " + p.Name)
	}

	profileLine := styleMuted.Render(fmt.Sprintf("ctx:%s  port:%d  host:%s", fmtInt(p.ContextSize), p.Port, p.Host))
	locationLine := styleMuted.Render(profileLocation(i.entry))
	fmt.Fprintf(w, "%s\n  %s\n  %s", title, profileLine, locationLine)
}

func NewProfileBrowserModel(entries []db.ProfileEntry, w, h int) ProfileBrowserModel {
	items := buildProfileBrowserItems(entries)
	listW := 76
	if w < listW+4 {
		listW = w - 4
	}
	if listW < 20 {
		listW = 20
	}
	l := list.New(items, profileBrowserDelegate{}, listW, h-8)
	l.Title = "Profiles"
	l.Styles.Title = styleTitle
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.StatusMessageLifetime = 0
	return ProfileBrowserModel{list: l, entries: entries, width: w, height: h, initialized: true}
}

func buildProfileBrowserItems(entries []db.ProfileEntry) []list.Item {
	items := make([]list.Item, 0, len(entries))
	for _, entry := range entries {
		items = append(items, profileBrowserItem{entry: entry})
	}
	return items
}

func profileLocation(entry db.ProfileEntry) string {
	model := entry.Model
	parts := []string{model.DisplayName}
	if model.ScanDir != "" {
		parts = append(parts, model.ScanDir)
	} else if model.GGUFPath != "" {
		parts = append(parts, model.GGUFPath)
	}
	return strings.Join(parts, " · ")
}

func (m ProfileBrowserModel) SetSize(w, h int) ProfileBrowserModel {
	if !m.initialized {
		return m
	}
	m.width, m.height = w, h
	listW := 76
	if w < listW+4 {
		listW = w - 4
	}
	if listW < 20 {
		listW = 20
	}
	m.list.SetSize(listW, h-8)
	return m
}

func (m ProfileBrowserModel) SetEntries(entries []db.ProfileEntry) ProfileBrowserModel {
	m.entries = entries
	m.statusMsg = ""
	m.list.SetItems(buildProfileBrowserItems(entries))
	return m
}

func (m ProfileBrowserModel) Update(msg tea.Msg) (ProfileBrowserModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ProfileBrowserModel) View() string {
	t := ActiveTheme
	status := ""
	if m.deleteConfirm {
		status = styleError.Render("Delete this profile? (y/n)")
	} else if m.statusMsg != "" {
		status = styleMuted.Render(m.statusMsg)
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		m.list.View(),
		status,
	)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m ProfileBrowserModel) Selected() (db.ProfileEntry, bool) {
	item, ok := m.list.SelectedItem().(profileBrowserItem)
	if !ok {
		return db.ProfileEntry{}, false
	}
	return item.entry, true
}

func (m ProfileBrowserModel) SelectedProfile() (db.ProfileEntry, bool) {
	return m.Selected()
}

func (m ProfileBrowserModel) IsFiltering() bool {
	return strings.Contains(m.list.FilterState().String(), "filtering")
}
