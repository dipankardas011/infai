package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/config"
	"github.com/dipankardas011/infai/model"
)

const logoASCII = `
██╗███╗   ██╗███████╗ █████╗ ██╗
██║████╗  ██║██╔════╝██╔══██╗██║
██║██╔██╗ ██║█████╗  ███████║██║
██║██║╚██╗██║██╔══╝  ██╔══██║██║
██║██║ ╚████║██║     ██║  ██║██║
╚═╝╚═╝  ╚═══╝╚═╝     ╚═╝  ╚═╝╚═╝`

// modelItem wraps ModelEntry for the bubbles list.
type modelItem struct {
	entry model.ModelEntry
}

func (m modelItem) FilterValue() string { return m.entry.DisplayName }
func (m modelItem) Title() string       { return m.entry.DisplayName }
func (m modelItem) Description() string {
	scanPart := styleMuted.Render("[" + m.entry.ScanDir + "]")
	if m.entry.Type != "" {
		return styleBadge.Render(m.entry.Type) + "  " + scanPart
	}
	return scanPart
}

type modelDelegate struct{}

func (d modelDelegate) Height() int                             { return 2 }
func (d modelDelegate) Spacing() int                            { return 1 }
func (d modelDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d modelDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(modelItem)
	if !ok {
		return
	}
	title := i.entry.DisplayName
	scanPart := styleMuted.Render("[" + i.entry.ScanDir + "]")
	desc := scanPart
	if i.entry.Type != "" {
		desc = styleBadge.Render(i.entry.Type) + "  " + scanPart
	}

	if index == m.Index() {
		title = styleSelected.Render("▶ " + title)
	} else {
		title = lipgloss.NewStyle().Foreground(colorText).Render("  " + title)
	}
	fmt.Fprintf(w, "%s\n  %s", title, desc)
}

// ModelListModel is screen 1.
type ModelListModel struct {
	list    list.Model
	entries []model.ModelEntry
	width   int
	height  int
}

func NewModelListModel(entries []model.ModelEntry, w, h int) ModelListModel {
	items := make([]list.Item, len(entries))
	for i, e := range entries {
		items[i] = modelItem{entry: e}
	}
	l := list.New(items, modelDelegate{}, w, h-4)
	l.Title = "infai"
	l.Styles.Title = styleTitle
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return ModelListModel{list: l, entries: entries, width: w, height: h}
}

func (m ModelListModel) SetSize(w, h int) ModelListModel {
	m.width, m.height = w, h
	listW := 60
	if w < 60 {
		listW = w - 4
	}
	m.list.SetSize(listW, h-8)
	return m
}

func (m ModelListModel) SetEntries(entries []model.ModelEntry) ModelListModel {
	m.entries = entries
	items := make([]list.Item, len(entries))
	for i, e := range entries {
		items[i] = modelItem{entry: e}
	}
	m.list.SetItems(items)
	return m
}

func (m ModelListModel) Update(msg tea.Msg) (ModelListModel, tea.Cmd) {
	if len(m.entries) == 0 {
		return m, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ModelListModel) View() string {
	if len(m.entries) == 0 {
		return m.emptyView()
	}
	t := ActiveTheme
	help := styleHelp.Render(
		"enter: select  r: rescan  /: filter  esc: back",
	)

	listView := m.list.View()

	content := lipgloss.JoinVertical(lipgloss.Left,
		listView,
		"\n"+help,
	)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m ModelListModel) emptyView() string {
	t := ActiveTheme
	logoStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(t.Secondary)
	hintStyle := lipgloss.NewStyle().Foreground(t.Muted).Italic(true)

	var sb strings.Builder
	sb.WriteString(logoStyle.Render(logoASCII))
	sb.WriteString("\n\n")
	sb.WriteString(versionStyle.Render("  " + config.Version()))
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("  no models found — press [f] to add scan folders"))
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("  t: theme  q: quit"))

	content := sb.String()
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m ModelListModel) Selected() (model.ModelEntry, bool) {
	item, ok := m.list.SelectedItem().(modelItem)
	if !ok {
		return model.ModelEntry{}, false
	}
	return item.entry, true
}

func (m ModelListModel) IsFiltering() bool {
	return strings.Contains(m.list.FilterState().String(), "filtering")
}
