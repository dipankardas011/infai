package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/internal/backend"
	"github.com/dipankardas011/infai/internal/db"
	"github.com/dipankardas011/infai/internal/model"
)

const (
	tabProfiles = iota
	tabRuns
	tabModels
	tabEngines
)

var tabNames = []string{"Profiles", "Runs", "Models", "Engines"}

// HomeModel is the tabbed home screen with 4 tabs.
type HomeModel struct {
	activeTab int
	service   *backend.Service

	profilesTab ProfilesTabModel
	runsTab     RunsTabModel
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
		runsTab:     NewRunsTabModel(w, h),
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
	m.runsTab = m.runsTab.SetSize(w, h)
	m.modelsTab = m.modelsTab.SetSize(w, h)
	m.enginesTab = m.enginesTab.SetSize(w, h)
	return m
}

func (m HomeModel) RefreshProfiles(recents []db.RecentEntry, all []db.ProfileEntry) HomeModel {
	m.profilesTab = m.profilesTab.SetData(recents, all)
	return m
}

func (m HomeModel) SetRuns(runs []RunSnapshot) HomeModel {
	m.runsTab = m.runsTab.SetRuns(runs)
	m.profilesTab = m.profilesTab.SetRuns(runs)
	return m
}

func (m HomeModel) SelectRun(id RunID) HomeModel {
	m.runsTab = m.runsTab.SetSelectedRun(id)
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

// InModalInput reports whether the active tab is capturing raw text input or
// showing a modal dialog, i.e. keys must not be treated as global shortcuts.
func (m HomeModel) InModalInput() bool {
	switch m.activeTab {
	case tabProfiles:
		return m.profilesTab.InModalInput()
	case tabModels:
		return m.modelsTab.InModalInput()
	case tabEngines:
		return m.enginesTab.InModalInput()
	}
	return false
}

func (m HomeModel) Update(msg tea.Msg) (HomeModel, tea.Cmd) {
	// Tab switching keys work whenever the active tab isn't capturing input.
	if key, ok := msg.(tea.KeyMsg); ok && !m.InModalInput() {
		switch key.String() {
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + len(tabNames)) % len(tabNames)
			return m, nil
		case "tab":
			m.activeTab = (m.activeTab + 1) % len(tabNames)
			return m, nil
		case "1":
			m.activeTab = tabProfiles
			return m, nil
		case "2":
			m.activeTab = tabRuns
			return m, nil
		case "3":
			m.activeTab = tabModels
			return m, nil
		case "4":
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
	case tabRuns:
		var cmd tea.Cmd
		m.runsTab, cmd = m.runsTab.Update(msg)
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

	// Tabs — numbered so the 1-4 jump keys are discoverable; Runs shows a live
	// count of active servers.
	labels := make([]string, len(tabNames))
	for i, name := range tabNames {
		labels[i] = fmt.Sprintf("%d %s", i+1, name)
	}
	if n := m.runsTab.activeCount(); n > 0 {
		labels[tabRuns] = fmt.Sprintf("%d %s (%d)", tabRuns+1, tabNames[tabRuns], n)
	}
	tabs := RenderTabs(labels, m.activeTab, innerW)

	// Tab content
	var body string
	switch m.activeTab {
	case tabProfiles:
		body = m.profilesTab.SetSize(innerW, bodyH).View()
	case tabRuns:
		body = m.runsTab.SetSize(innerW, bodyH).View()
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
