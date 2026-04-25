package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/model"
)

// modelItem wraps ModelEntry for the bubbles list.
type modelItem struct {
	entry model.ModelEntry
}

func (m modelItem) FilterValue() string { return m.entry.DisplayName }
func (m modelItem) Title() string       { return m.entry.DisplayName }
func (m modelItem) Description() string {
	if m.entry.MmprojPath != "" {
		return styleBadge.Render("mmproj") + "  " + styleMuted.Render(m.entry.GGUFPath)
	}
	return styleMuted.Render(m.entry.GGUFPath)
}

type modelDelegate struct{}

func (d modelDelegate) Height() int                             { return 2 }
func (d modelDelegate) Spacing() int                           { return 1 }
func (d modelDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d modelDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(modelItem)
	if !ok {
		return
	}
	title := i.entry.DisplayName
	desc := ""
	if i.entry.MmprojPath != "" {
		desc = styleBadge.Render("mmproj") + "  " + styleMuted.Render(i.entry.GGUFPath)
	} else {
		desc = styleMuted.Render(i.entry.GGUFPath)
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
	return ModelListModel{list: l, entries: entries}
}

func (m ModelListModel) SetSize(w, h int) ModelListModel {
	m.list.SetSize(w, h-4)
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
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ModelListModel) View() string {
	help := styleHelp.Render(
		"enter: select  r: rescan  t: theme(" + ActiveTheme.Name + ")  /: filter  q: quit",
	)
	return m.list.View() + "\n" + help
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
