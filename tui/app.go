package tui

import (
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/scanner"

	tea "github.com/charmbracelet/bubbletea"
)

type screenKind int

const (
	screenModelList screenKind = iota
	screenProfileList
	screenProfileEdit
	screenConfirm
	screenServerRunning
)

// Cross-screen transition messages.
type scanDoneMsg struct{ entries []model.ModelEntry }
type saveProfileMsg struct{ profile model.Profile }
type deleteProfileMsg struct{ id int64 }

// AppModel is the root bubbletea model.
type AppModel struct {
	screen    screenKind
	database  *db.DB
	serverBin string
	modelsDir string
	width     int
	height    int
	errMsg    string

	modelList   ModelListModel
	profileList ProfileListModel
	profileEdit ProfileEditModel
	confirm     ConfirmModel
	server      ServerModel

	selectedModel   model.ModelEntry
	selectedProfile model.Profile
}

func NewApp(database *db.DB, serverBin, modelsDir string, entries []model.ModelEntry, w, h int) AppModel {
	return AppModel{
		database:  database,
		serverBin: serverBin,
		modelsDir: modelsDir,
		width:     w,
		height:    h,
		modelList: NewModelListModel(entries, w, h),
	}
}

func (a AppModel) Init() tea.Cmd { return nil }

func (a AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.modelList = a.modelList.SetSize(a.width, a.height)
		a.profileList = a.profileList.SetSize(a.width, a.height)
		a.profileEdit = a.profileEdit.SetSize(a.width, a.height)
		a.confirm = a.confirm.SetWidth(a.width)
		a.server = a.server.SetSize(a.width, a.height)
		return a, nil

	case scanDoneMsg:
		for i := range msg.entries {
			if err := a.database.UpsertModel(&msg.entries[i]); err != nil {
				a.errMsg = err.Error()
			}
		}
		a.modelList = a.modelList.SetEntries(msg.entries)
		return a, nil

	case saveProfileMsg:
		p := msg.profile
		if err := a.database.UpsertProfile(&p); err != nil {
			a.errMsg = err.Error()
			return a, nil
		}
		profiles, _ := a.database.ListProfiles(a.selectedModel.ID)
		a.profileList = a.profileList.SetProfiles(profiles)
		a.screen = screenProfileList
		return a, nil

	case deleteProfileMsg:
		if err := a.database.DeleteProfile(msg.id); err != nil {
			a.errMsg = err.Error()
			return a, nil
		}
		profiles, _ := a.database.ListProfiles(a.selectedModel.ID)
		a.profileList = a.profileList.SetProfiles(profiles)
		return a, nil

	// Server log streaming messages — only process when in server screen.
	case logLineMsg:
		if a.screen == screenServerRunning {
			var cmd tea.Cmd
			a.server, cmd = a.server.HandleLogLine(string(msg))
			return a, cmd
		}
		return a, nil

	case serverExitMsg:
		if a.screen == screenServerRunning {
			a.server = a.server.SetExited(msg.err)
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.screen == screenServerRunning {
				a.server = a.server.Stop()
				return a, nil
			}
			return a, tea.Quit
		}
		if a.screen == screenModelList && msg.String() == "q" && !a.modelList.IsFiltering() {
			return a, tea.Quit
		}

		switch a.screen {
		case screenModelList:
			return a.updateModelList(msg)
		case screenProfileList:
			return a.updateProfileList(msg)
		case screenProfileEdit:
			return a.updateProfileEdit(msg)
		case screenConfirm:
			return a.updateConfirm(msg)
		case screenServerRunning:
			return a.updateServer(msg)
		}
	}

	// Non-key msgs: delegate to active sub-model.
	switch a.screen {
	case screenModelList:
		var cmd tea.Cmd
		a.modelList, cmd = a.modelList.Update(msg)
		return a, cmd
	case screenProfileList:
		var cmd tea.Cmd
		a.profileList, cmd = a.profileList.Update(msg)
		return a, cmd
	case screenProfileEdit:
		var cmd tea.Cmd
		a.profileEdit, cmd = a.profileEdit.Update(msg)
		return a, cmd
	case screenServerRunning:
		var cmd tea.Cmd
		a.server, cmd = a.server.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a AppModel) updateModelList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if entry, ok := a.modelList.Selected(); ok {
			a.selectedModel = entry
			profiles, _ := a.database.ListProfiles(entry.ID)
			a.profileList = NewProfileListModel(entry, profiles, a.width, a.height)
			a.screen = screenProfileList
			a.errMsg = ""
			return a, nil
		}
	case "r":
		return a, func() tea.Msg {
			entries, _ := scanner.Scan(a.modelsDir)
			return scanDoneMsg{entries: entries}
		}
	case "t":
		name := CycleTheme()
		a.database.SetSetting("theme", name)
		return a, nil
	}
	var cmd tea.Cmd
	a.modelList, cmd = a.modelList.Update(msg)
	return a, cmd
}

func (a AppModel) updateProfileList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		a.screen = screenModelList
		a.errMsg = ""
		return a, nil

	case "enter":
		profile, isNew, ok := a.profileList.Selected()
		if !ok {
			break
		}
		if isNew {
			a.profileEdit = NewProfileEditModel(a.selectedModel, nil, a.width, a.height)
			a.screen = screenProfileEdit
			return a, nil
		}
		a.selectedProfile = profile
		a.confirm = NewConfirmModel(a.serverBin, a.selectedModel, profile, a.width)
		a.screen = screenConfirm
		a.errMsg = ""
		return a, nil

	case "e":
		if profile, ok := a.profileList.SelectedProfile(); ok {
			a.profileEdit = NewProfileEditModel(a.selectedModel, &profile, a.width, a.height)
			a.screen = screenProfileEdit
			return a, nil
		}

	case "d":
		if a.profileList.deleteConfirm {
			break
		}
		if profile, ok := a.profileList.SelectedProfile(); ok {
			a.profileList.deleteConfirm = true
			a.profileList.deleteID = profile.ID
			return a, nil
		}

	case "y":
		if a.profileList.deleteConfirm {
			id := a.profileList.deleteID
			a.profileList.deleteConfirm = false
			a.profileList.deleteID = 0
			return a, func() tea.Msg { return deleteProfileMsg{id: id} }
		}

	case "n":
		if a.profileList.deleteConfirm {
			a.profileList.deleteConfirm = false
			a.profileList.deleteID = 0
			return a, nil
		}
	}
	var cmd tea.Cmd
	a.profileList, cmd = a.profileList.Update(msg)
	return a, cmd
}

func (a AppModel) updateProfileEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = screenProfileList
		a.profileEdit.errMsg = ""
		return a, nil
	case "ctrl+s":
		p, err := a.profileEdit.ToProfile()
		if err != nil {
			a.profileEdit.errMsg = err.Error()
			return a, nil
		}
		return a, func() tea.Msg { return saveProfileMsg{profile: p} }
	}
	var cmd tea.Cmd
	a.profileEdit, cmd = a.profileEdit.Update(msg)
	return a, cmd
}

func (a AppModel) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = screenProfileList
		return a, nil
	case "enter":
		args := a.confirm.Args()
		sm, cmd, err := NewServerModel(
			args,
			a.selectedProfile.Name,
			a.selectedModel.DisplayName,
			a.selectedProfile.Port,
			a.width,
			a.height,
		)
		if err != nil {
			a.errMsg = err.Error()
			a.screen = screenProfileList
			return a, nil
		}
		a.server = sm
		a.screen = screenServerRunning
		return a, cmd
	}
	return a, nil
}

func (a AppModel) updateServer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s":
		a.server = a.server.Stop()
		return a, nil
	case "esc":
		a.server = a.server.Stop()
		a.screen = screenProfileList
		return a, nil
	}
	// Pass scrolling keys to viewport.
	var cmd tea.Cmd
	a.server, cmd = a.server.Update(msg)
	return a, cmd
}

func (a AppModel) View() string {
	errBanner := ""
	if a.errMsg != "" {
		errBanner = styleError.Render("error: "+a.errMsg) + "\n"
	}
	switch a.screen {
	case screenModelList:
		return errBanner + a.modelList.View()
	case screenProfileList:
		return errBanner + a.profileList.View()
	case screenProfileEdit:
		return errBanner + a.profileEdit.View()
	case screenConfirm:
		return errBanner + a.confirm.View()
	case screenServerRunning:
		return a.server.View()
	}
	return ""
}
