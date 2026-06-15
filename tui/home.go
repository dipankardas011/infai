package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
)

const (
	tabProfiles = iota
	tabModels
	tabEngines
)

var tabNames = []string{"Profiles", "Models", "Engines"}

// HomeModel is the tabbed home screen with 3 tabs.
type HomeModel struct {
	activeTab int
	service   *backend.Service

	profilesTab ProfilesTabModel
	modelsTab   ModelsTabModel
	enginesTab  EnginesTabModel

	width  int
	height int
}

func NewHomeModel(
	service *backend.Service,
	scanDirs []string,
	entries []model.ModelEntry,
	recents []db.RecentEntry,
	allProfiles []db.ProfileEntry,
	w, h int,
) HomeModel {
	return HomeModel{
		activeTab:   tabProfiles,
		service:     service,
		profilesTab: NewProfilesTabModel(recents, allProfiles, w, h),
		modelsTab:   NewModelsTabModel(service, scanDirs, w, h),
		enginesTab:  NewEnginesTabModel(service, w, h),
		width:       w,
		height:      h,
	}
}

func (m HomeModel) SetSize(w, h int) HomeModel {
	m.width = w
	m.height = h
	m.profilesTab = m.profilesTab.SetSize(w, h)
	m.modelsTab = m.modelsTab.SetSize(w, h)
	m.enginesTab = m.enginesTab.SetSize(w, h)
	return m
}

func (m HomeModel) RefreshProfiles(recents []db.RecentEntry, all []db.ProfileEntry) HomeModel {
	m.profilesTab = m.profilesTab.SetData(recents, all)
	return m
}

func (m HomeModel) RefreshModels(dirs []string) HomeModel {
	models, _ := m.service.ListModels()
	m.modelsTab = NewModelsTabModel(m.service, dirs, m.width, m.height)
	m.modelsTab.modelCnt = len(models)
	return m
}

func (m HomeModel) RefreshEngines() HomeModel {
	m.enginesTab = NewEnginesTabModel(m.service, m.width, m.height)
	return m
}

func (m HomeModel) Update(msg tea.Msg) (HomeModel, tea.Cmd) {
	// Tab switching keys work regardless of tab state
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + len(tabNames)) % len(tabNames)
			return m, nil
		case "tab":
			m.activeTab = (m.activeTab + 1) % len(tabNames)
			return m, nil
		}
		// Number keys for tabs
		if key.String() == "1" {
			m.activeTab = tabProfiles
			return m, nil
		}
		if key.String() == "2" {
			m.activeTab = tabModels
			return m, nil
		}
		if key.String() == "3" {
			m.activeTab = tabEngines
			return m, nil
		}
	}

	// Delegate to active tab
	switch m.activeTab {
	case tabProfiles:
		var cmd tea.Cmd
		m.profilesTab, cmd = m.profilesTab.Update(msg)
		return m, cmd
	case tabModels:
		var cmd tea.Cmd
		m.modelsTab, cmd = m.modelsTab.Update(msg)
		return m, cmd
	case tabEngines:
		var cmd tea.Cmd
		m.enginesTab, cmd = m.enginesTab.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m HomeModel) View() string {
	t := ActiveTheme

	// m.height = space AFTER AppModel reserves global header/toast/help lines.
	// Home reserves exactly 2 lines for tabs + divider. Everything else
	// must scroll inside the active tab; it must never grow this view.
	bodyArea := NewArea(m.width, m.height).ReserveHeight(2) // tabs + divider
	bodyH := max(bodyArea.H, 1)
	innerW := m.width

	// Tabs
	tabs := RenderTabs(tabNames, m.activeTab, innerW)

	// Tab content
	var body string
	switch m.activeTab {
	case tabProfiles:
		body = m.profilesTab.SetSize(innerW, bodyH).View()
	case tabModels:
		body = m.modelsTab.SetSize(innerW, bodyH).View()
	case tabEngines:
		body = m.enginesTab.SetSize(innerW, bodyH).View()
	}

	// Never allow tab content to push header/tabs out of the window.
	body = ClampHeight(bodyArea, body)

	// Divider below tabs
	divStyle := lipgloss.NewStyle().Foreground(t.Muted)
	divider := divStyle.Render(horizontalLine(innerW))

	return tabs + "\n" + divider + "\n" + body
}

// Messages the home model can produce for AppModel
type homeMsgLaunch struct {
	model   model.ModelEntry
	profile model.Profile
}
type homeMsgNewProfile struct{}
type homeMsgEditProfile struct {
	model   model.ModelEntry
	profile model.Profile
}
type homeMsgDeleteProfile struct {
	id int64
}
type homeMsgSyncDone struct {
	removed, updated int
	err              error
}
type homeMsgExecutorUpdated struct {
	path string
}
