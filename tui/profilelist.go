package tui

import (
	"fmt"
	"io"
	"strconv"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/model"
)


type profileItem struct {
	profile model.Profile
	isNew   bool
}

func (p profileItem) FilterValue() string {
	if p.isNew {
		return "new profile"
	}
	return p.profile.Name
}

type profileDelegate struct{}

func (d profileDelegate) Height() int                             { return 2 }
func (d profileDelegate) Spacing() int                           { return 1 }
func (d profileDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d profileDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(profileItem)
	if !ok {
		return
	}
	selected := index == m.Index()

	if i.isNew {
		title := lipgloss.NewStyle().Foreground(colorSuccess).Render("  [ + new profile ]")
		if selected {
			title = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("▶ [ + new profile ]")
		}
		fmt.Fprintf(w, "%s\n", title)
		return
	}

	p := i.profile
	summary := buildSummary(p)
	title := lipgloss.NewStyle().Foreground(colorText).Render("  " + p.Name)
	if selected {
		title = styleSelected.Render("▶ " + p.Name)
	}
	fmt.Fprintf(w, "%s\n  %s", title, styleMuted.Render(summary))
}

func buildSummary(p model.Profile) string {
	s := fmt.Sprintf("port:%d  ctx:%s  ngl:%s", p.Port, fmtInt(p.ContextSize), p.NGL)
	if p.FlashAttn {
		s += "  flash:on"
	}
	if p.UseMmproj {
		s += "  mmproj:on"
	}
	if p.CacheTypeK != nil {
		s += "  k:" + *p.CacheTypeK
	}
	return s
}

func fmtInt(n int) string {
	if n >= 1<<20 {
		return strconv.Itoa(n/1024/1024) + "M"
	}
	if n >= 1<<10 {
		return strconv.Itoa(n/1024) + "K"
	}
	return strconv.Itoa(n)
}

// ProfileListModel is screen 2.
type ProfileListModel struct {
	list          list.Model
	profiles      []model.Profile
	modelEntry    model.ModelEntry
	deleteConfirm bool
	deleteID      int64
	statusMsg     string
	initialized   bool
}

func NewProfileListModel(m model.ModelEntry, profiles []model.Profile, w, h int) ProfileListModel {
	items := buildProfileItems(profiles)
	l := list.New(items, profileDelegate{}, w, h-5)
	l.Title = "Profiles: " + m.DisplayName
	l.Styles.Title = styleTitle
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	return ProfileListModel{list: l, profiles: profiles, modelEntry: m, initialized: true}
}

func buildProfileItems(profiles []model.Profile) []list.Item {
	items := []list.Item{profileItem{isNew: true}}
	for _, p := range profiles {
		items = append(items, profileItem{profile: p})
	}
	return items
}

func (m ProfileListModel) SetSize(w, h int) ProfileListModel {
	if !m.initialized {
		return m
	}
	m.list.SetSize(w, h-5)
	return m
}

func (m ProfileListModel) SetProfiles(profiles []model.Profile) ProfileListModel {
	m.profiles = profiles
	m.list.SetItems(buildProfileItems(profiles))
	return m
}

func (m ProfileListModel) Update(msg tea.Msg) (ProfileListModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ProfileListModel) View() string {
	status := ""
	if m.deleteConfirm {
		status = styleError.Render("Delete this profile? (y/n)")
	} else if m.statusMsg != "" {
		status = styleMuted.Render(m.statusMsg)
	}
	help := styleHelp.Render("enter: launch/new  e: edit  d: delete  esc: back")
	footer := status + "\n" + help
	return m.list.View() + "\n" + footer
}

func (m ProfileListModel) Selected() (model.Profile, bool, bool) {
	item, ok := m.list.SelectedItem().(profileItem)
	if !ok {
		return model.Profile{}, false, false
	}
	return item.profile, item.isNew, true
}

func (m ProfileListModel) SelectedProfile() (model.Profile, bool) {
	item, ok := m.list.SelectedItem().(profileItem)
	if !ok || item.isNew {
		return model.Profile{}, false
	}
	return item.profile, true
}

