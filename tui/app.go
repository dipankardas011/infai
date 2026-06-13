package tui

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/scanner"
)

type toastTickMsg struct{}

func toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastTickMsg{} })
}

type screenKind int

const (
	screenHome screenKind = iota
	screenModelList
	screenProfileBrowser
	screenProfileList
	screenProfileEdit
	screenConfirm
	screenServerRunning
	screenExplore
	screenExecutor
	screenThemeSelector
)

type modelListPurpose int

const (
	modelListBrowse modelListPurpose = iota
	modelListPickForProfile
)

// Cross-screen transition messages.
type scanDoneMsg struct {
	entries []model.ModelEntry
	err     error
}
type saveProfileMsg struct{ profile model.Profile }
type deleteProfileMsg struct{ id int64 }
type syncDoneMsg struct {
	removed, updated int
	err              error
}

// AppModel is the root bubbletea model.
type AppModel struct {
	screen       screenKind
	database     *db.DB
	serverBin    string
	scanDirs     []string
	width        int
	height       int
	errMsg       string
	errMsgTicks  int
	quitArmed    bool
	help         help.Model
	showFullHelp bool

	modelList      ModelListModel
	profileBrowser ProfileBrowserModel
	profileList    ProfileListModel
	profileEdit    ProfileEditModel
	confirm        ConfirmModel
	server         ServerModel
	explore        ExploreModel
	executor       ExecutorModel
	home           HomeModel
	themeSelector  ThemeSelectorModel

	selectedModel     model.ModelEntry
	selectedProfile   model.Profile
	modelListPurpose  modelListPurpose
	profileEditReturn screenKind
	confirmReturn     screenKind
}

func NewApp(database *db.DB, serverBin string, scanDirs []string, entries []model.ModelEntry, w, h int) AppModel {
	var startupErrs []string

	recent, err := database.ListRecents(2)
	if err != nil {
		startupErrs = append(startupErrs, fmt.Sprintf("failed to load recents: %v", err))
	}

	dbBin, err := database.GetDefaultExecutorPath()
	if err == nil && dbBin != "" {
		serverBin = dbBin
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		startupErrs = append(startupErrs, fmt.Sprintf("failed to load executor: %v", err))
	}

	profiles, err := database.ListAllProfiles()
	if err != nil {
		startupErrs = append(startupErrs, fmt.Sprintf("failed to load profiles: %v", err))
	}

	return AppModel{
		database:       database,
		serverBin:      serverBin,
		scanDirs:       scanDirs,
		width:          w,
		height:         h,
		errMsg:         strings.Join(startupErrs, "; "),
		help:           help.New(),
		home:           NewHomeModel(recent, scanDirs, serverBin, w, h),
		modelList:      NewModelListModel(entries, w, h),
		profileBrowser: NewProfileBrowserModel(profiles, w, h),
		executor:       NewExecutorModel(database, serverBin, w, h),
		themeSelector:  NewThemeSelectorModel(w, h),
	}
}

func (a *AppModel) Init() tea.Cmd { return toastTick() }

func (a *AppModel) setErr(msg string) {
	a.errMsg = msg
	a.errMsgTicks = 0
}

func (a *AppModel) refreshHome() {
	recent, err := a.database.ListRecents(2)
	if err != nil {
		a.setErr(err.Error())
		return
	}
	a.home = NewHomeModel(recent, a.scanDirs, a.serverBin, a.width, a.height)
}

func (a *AppModel) refreshProfileBrowser() {
	profiles, err := a.database.ListAllProfiles()
	if err != nil {
		a.setErr(err.Error())
		return
	}
	a.profileBrowser = a.profileBrowser.SetEntries(profiles).SetSize(a.width, a.height)
}

func (a *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case toastTickMsg:
		if a.errMsg != "" {
			a.errMsgTicks++
			if a.errMsgTicks >= 4 {
				a.errMsg = ""
				a.errMsgTicks = 0
			}
		}
		return a, toastTick()

	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.help.Width = msg.Width
		a.home = a.home.SetSize(a.width, a.height)
		a.modelList = a.modelList.SetSize(a.width, a.height)
		a.profileBrowser = a.profileBrowser.SetSize(a.width, a.height)
		a.profileList = a.profileList.SetSize(a.width, a.height)
		a.profileEdit = a.profileEdit.SetSize(a.width, a.height)
		a.confirm = a.confirm.SetSize(a.width, a.height)
		a.server = a.server.SetSize(a.width, a.height)
		a.explore = a.explore.SetSize(a.width, a.height)
		a.executor = a.executor.SetSize(a.width, a.height)
		a.themeSelector = a.themeSelector.SetSize(a.width, a.height)
		return a, nil

	case scanDoneMsg:
		if msg.err != nil {
			a.setErr(msg.err.Error())
			return a, nil
		}
		for i := range msg.entries {
			if err := a.database.UpsertModel(&msg.entries[i]); err != nil {
				a.setErr(err.Error())
			}
		}
		a.modelList = a.modelList.SetEntries(msg.entries)
		a.refreshProfileBrowser()
		a.refreshHome()
		return a, nil

	case saveProfileMsg:
		p := msg.profile
		if err := a.database.UpsertProfile(&p); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		switch a.profileEditReturn {
		case screenProfileBrowser:
			a.refreshProfileBrowser()
			a.screen = screenProfileBrowser
		default:
			profiles, err := a.database.ListProfiles(a.selectedModel.ID)
			if err != nil {
				a.setErr(err.Error())
				return a, nil
			}
			a.profileList = a.profileList.SetProfiles(profiles)
			a.screen = screenProfileList
		}
		return a, nil

	case deleteProfileMsg:
		if err := a.database.DeleteProfile(msg.id); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		if a.screen == screenProfileBrowser {
			a.refreshProfileBrowser()
			return a, nil
		}
		profiles, err := a.database.ListProfiles(a.selectedModel.ID)
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.profileList = a.profileList.SetProfiles(profiles)
		return a, nil

	case ExecutorSavedMsg:
		a.serverBin = msg.Bin
		a.refreshHome()
		a.screen = screenHome
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

	case stopTimeoutMsg:
		if !a.server.stopped && a.server.stopping {
			a.server = a.server.ForceKill()
			a.setErr("server unresponsive — sent SIGKILL")
		}
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.screen == screenServerRunning {
				if a.server.stopping || a.server.stopped {
					a.server = a.server.ForceKill()
					return a, tea.Quit
				}
				var cmd tea.Cmd
				a.server, cmd = a.server.Stop()
				a.setErr("shutting down server (SIGTERM)... ctrl+c again to force quit")
				return a, cmd
			}
			if a.quitArmed {
				return a, tea.Quit
			}
			a.quitArmed = true
			a.setErr("press ctrl+c again to quit")
			return a, nil
		}
		if a.quitArmed {
			a.quitArmed = false
			a.errMsg = ""
		}
		if a.screen == screenHome && msg.String() == "q" {
			return a, tea.Quit
		}
		if msg.String() == "?" && a.screen != screenProfileEdit && a.screen != screenExecutor {
			a.showFullHelp = !a.showFullHelp
			a.help.ShowAll = a.showFullHelp
			return a, nil
		}

		switch a.screen {
		case screenHome:
			return a.updateHome(msg)
		case screenModelList:
			return a.updateModelList(msg)
		case screenProfileBrowser:
			return a.updateProfileBrowser(msg)
		case screenProfileList:
			return a.updateProfileList(msg)
		case screenProfileEdit:
			return a.updateProfileEdit(msg)
		case screenConfirm:
			return a.updateConfirm(msg)
		case screenServerRunning:
			return a.updateServer(msg)
		case screenExplore:
			return a.updateExplore(msg)
		case screenExecutor:
			return a.updateExecutor(msg)
		case screenThemeSelector:
			return a.updateThemeSelector(msg)
		}
	}

	return a.handleNonKeyMsg(msg)
}

func (a *AppModel) handleNonKeyMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Non-key msgs: delegate to active sub-model.
	switch a.screen {
	case screenHome:
		var cmd tea.Cmd
		a.home, cmd = a.home.Update(msg)
		return a, cmd
	case screenModelList:
		var cmd tea.Cmd
		a.modelList, cmd = a.modelList.Update(msg)
		return a, cmd
	case screenProfileBrowser:
		var cmd tea.Cmd
		a.profileBrowser, cmd = a.profileBrowser.Update(msg)
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
	case screenExplore:
		var cmd tea.Cmd
		a.explore, cmd = a.explore.Update(msg)
		return a, cmd
	case screenExecutor:
		var cmd tea.Cmd
		a.executor, cmd = a.executor.Update(msg)
		return a, cmd
	case screenThemeSelector:
		var cmd tea.Cmd
		a.themeSelector, cmd = a.themeSelector.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *AppModel) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a":
		a.refreshProfileBrowser()
		a.screen = screenProfileBrowser
		return a, nil
	case "f":
		a.explore.Close()
		a.explore = NewExploreModel(a.database, a.scanDirs, a.width, a.height)
		a.screen = screenExplore
		return a, nil
	case "c":
		a.executor = NewExecutorModel(a.database, a.serverBin, a.width, a.height)
		a.screen = screenExecutor
		return a, nil
	case "t":
		a.themeSelector = NewThemeSelectorModel(a.width, a.height)
		a.screen = screenThemeSelector
		return a, nil
	case "enter":
		if entry, ok := a.home.Selected(); ok {
			a.selectedModel = entry.Model
			a.selectedProfile = entry.Profile
			a.confirm = NewConfirmModel(a.serverBin, entry.Model, entry.Profile, a.width, a.height)
			a.confirmReturn = screenHome
			a.screen = screenConfirm
			a.errMsg = ""
			return a, nil
		}
	}
	var cmd tea.Cmd
	a.home, cmd = a.home.Update(msg)
	return a, cmd
}

func (a *AppModel) updateThemeSelector(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = screenHome
		return a, nil
	case "enter":
		theme := a.themeSelector.SelectedTheme()
		SetTheme(theme.Name)
		a.database.SetSetting("theme", theme.Name)
		a.screen = screenHome
		return a, nil
	}
	var cmd tea.Cmd
	a.themeSelector, cmd = a.themeSelector.Update(msg)
	return a, cmd
}

func (a *AppModel) updateExecutor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		var cmd tea.Cmd
		a.executor, cmd = a.executor.SaveAndExit()
		a.refreshHome()
		a.screen = screenHome
		return a, cmd
	}
	var cmd tea.Cmd
	a.executor, cmd = a.executor.Update(msg)
	return a, cmd
}

func (a *AppModel) updateModelList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		if !a.modelList.IsFiltering() {
			return a, nil
		}
	case "esc":
		if !a.modelList.IsFiltering() {
			if a.modelListPurpose == modelListPickForProfile {
				a.refreshProfileBrowser()
				a.screen = screenProfileBrowser
				return a, nil
			}
			a.refreshHome()
			a.screen = screenHome
			return a, nil
		}
	case "enter":
		if entry, ok := a.modelList.Selected(); ok {
			a.selectedModel = entry
			if a.modelListPurpose == modelListPickForProfile {
				a.profileEdit = NewProfileEditModel(entry, nil, a.width, a.height)
				a.profileEditReturn = screenProfileBrowser
				a.screen = screenProfileEdit
				a.errMsg = ""
				return a, nil
			}
			profiles, err := a.database.ListProfiles(entry.ID)
			if err != nil {
				a.setErr(err.Error())
				return a, nil
			}
			a.profileList = NewProfileListModel(entry, profiles, a.width, a.height)
			a.screen = screenProfileList
			a.errMsg = ""
			return a, nil
		}
	case "r":
		return a, func() tea.Msg {
			entries, err := scanner.Scan(a.scanDirs)
			return scanDoneMsg{entries: entries, err: err}
		}
	}
	var cmd tea.Cmd
	a.modelList, cmd = a.modelList.Update(msg)
	return a, cmd
}

func (a *AppModel) openProfileModelPicker() (tea.Model, tea.Cmd) {
	entries, err := a.database.ListModels()
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	if len(entries) == 0 {
		a.setErr("no models found - press [f] on home to add scan folders")
		return a, nil
	}
	a.modelList = NewModelListModel(entries, a.width, a.height).SetTitle("Choose model for new profile")
	a.modelListPurpose = modelListPickForProfile
	a.screen = screenModelList
	return a, nil
}

func (a *AppModel) updateProfileBrowser(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.profileBrowser.IsFiltering() {
		if msg.String() == "esc" {
			var cmd tea.Cmd
			a.profileBrowser, cmd = a.profileBrowser.Update(msg)
			return a, cmd
		}
		var cmd tea.Cmd
		a.profileBrowser, cmd = a.profileBrowser.Update(msg)
		return a, cmd
	}

	if a.profileBrowser.deleteConfirm {
		switch msg.String() {
		case "y":
			id := a.profileBrowser.deleteID
			a.profileBrowser.deleteConfirm = false
			a.profileBrowser.deleteID = 0
			return a, func() tea.Msg { return deleteProfileMsg{id: id} }
		case "n", "esc":
			a.profileBrowser.deleteConfirm = false
			a.profileBrowser.deleteID = 0
			return a, nil
		default:
			return a, nil
		}
	}

	switch msg.String() {
	case "esc", "backspace":
		a.refreshHome()
		a.screen = screenHome
		a.errMsg = ""
		return a, nil
	case "n":
		return a.openProfileModelPicker()
	case "enter":
		entry, ok := a.profileBrowser.Selected()
		if !ok {
			break
		}
		profile, err := a.database.GetProfile(entry.Profile.ID)
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.selectedModel = entry.Model
		a.selectedProfile = profile
		a.confirm = NewConfirmModel(a.serverBin, entry.Model, profile, a.width, a.height)
		a.confirmReturn = screenProfileBrowser
		a.screen = screenConfirm
		a.errMsg = ""
		return a, nil
	case "e":
		if entry, ok := a.profileBrowser.SelectedProfile(); ok {
			profile, err := a.database.GetProfile(entry.Profile.ID)
			if err != nil {
				a.setErr(err.Error())
				return a, nil
			}
			a.selectedModel = entry.Model
			a.profileEdit = NewProfileEditModel(entry.Model, &profile, a.width, a.height)
			a.profileEditReturn = screenProfileBrowser
			a.screen = screenProfileEdit
			return a, nil
		}
	case "d":
		if a.profileBrowser.deleteConfirm {
			break
		}
		if entry, ok := a.profileBrowser.SelectedProfile(); ok {
			a.profileBrowser.deleteConfirm = true
			a.profileBrowser.deleteID = entry.Profile.ID
			return a, nil
		}
	}
	var cmd tea.Cmd
	a.profileBrowser, cmd = a.profileBrowser.Update(msg)
	return a, cmd
}

func (a *AppModel) updateProfileList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return a, nil
	case "esc", "backspace":
		a.refreshHome()
		a.screen = screenHome
		a.errMsg = ""
		return a, nil

	case "enter":
		profile, isNew, ok := a.profileList.Selected()
		if !ok {
			break
		}
		if isNew {
			a.profileEdit = NewProfileEditModel(a.selectedModel, nil, a.width, a.height)
			a.profileEditReturn = screenProfileList
			a.screen = screenProfileEdit
			return a, nil
		}
		a.selectedProfile = profile
		a.confirm = NewConfirmModel(a.serverBin, a.selectedModel, profile, a.width, a.height)
		a.confirmReturn = screenProfileList
		a.screen = screenConfirm
		a.errMsg = ""
		return a, nil

	case "e":
		if profile, ok := a.profileList.SelectedProfile(); ok {
			a.profileEdit = NewProfileEditModel(a.selectedModel, &profile, a.width, a.height)
			a.profileEditReturn = screenProfileList
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

func (a *AppModel) updateProfileEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = a.profileEditReturn
		if a.screen == screenHome {
			a.screen = screenProfileList
		}
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

func (a *AppModel) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = a.confirmReturn
		if a.screen == screenHome {
			a.refreshHome()
		}
		return a, nil
	case "enter":
		if a.confirm.command == "" {
			a.setErr("no command configured - press [c] on home screen")
			a.screen = screenHome
			return a, nil
		}
		args := a.confirm.Args()
		if len(args) == 0 || args[0] == "" {
			a.setErr("executor path not set - press [c] on home screen")
			a.screen = screenHome
			return a, nil
		}
		_ = a.database.MarkRecent(a.selectedModel.ID, a.selectedProfile.ID)
		sm, cmd, err := NewServerModel(
			args,
			a.selectedProfile.Name,
			a.selectedModel.DisplayName,
			a.selectedModel.Type,
			a.selectedProfile.ContextSize,
			a.selectedProfile.Host,
			a.selectedProfile.Port,
			a.width,
			a.height,
		)
		if err != nil {
			a.setErr(err.Error())
			a.screen = screenHome
			a.refreshHome()
			return a, nil
		}
		a.server = sm
		a.screen = screenServerRunning
		return a, cmd
	}
	return a, nil
}

func (a *AppModel) updateServer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s":
		if a.server.stopped || a.server.stopping {
			return a, nil
		}
		var cmd tea.Cmd
		a.server, cmd = a.server.Stop()
		return a, cmd
	case "r":
		if !a.server.stopped || a.server.stopping {
			return a, nil
		}
		sm, cmd, err := a.server.Restart()
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.server = sm
		return a, cmd
	case "esc":
		if a.server.stopped {
			a.refreshHome()
			a.screen = screenHome
			return a, nil
		}
		var cmd tea.Cmd
		a.server, cmd = a.server.Stop()
		a.refreshHome()
		a.screen = screenHome
		return a, cmd
	}
	// Pass scrolling keys to viewport.
	var cmd tea.Cmd
	a.server, cmd = a.server.Update(msg)
	return a, cmd
}

func (a *AppModel) updateExplore(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.scanDirs = a.explore.Dirs()
		a.refreshHome()
		entries, err := a.database.ListModels()
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.modelList = a.modelList.SetEntries(entries)
		a.screen = screenHome
		return a, nil
	}
	var cmd tea.Cmd
	a.explore, cmd = a.explore.Update(msg)
	return a, cmd
}

func (a *AppModel) View() string {
	toast := ""
	toastLines := 0
	if a.errMsg != "" {
		toast = styleError.Render("⚠ "+a.errMsg) + "\n"
		toastLines = 2
	}

	helpView := a.helpView()
	helpLines := 0
	if helpView != "" {
		helpLines = 1
		if a.showFullHelp {
			helpLines = 3
		}
	}
	reservedH := toastLines + helpLines
	innerH := max(a.height-reservedH, 5)

	var body string
	switch a.screen {
	case screenHome:
		body = a.home.SetSize(a.width, innerH).View()
	case screenModelList:
		body = a.modelList.SetSize(a.width, innerH).View()
	case screenProfileBrowser:
		body = a.profileBrowser.SetSize(a.width, innerH).View()
	case screenProfileList:
		body = a.profileList.SetSize(a.width, innerH).View()
	case screenProfileEdit:
		body = a.profileEdit.SetSize(a.width, innerH).View()
	case screenConfirm:
		body = a.confirm.SetSize(a.width, innerH).View()
	case screenServerRunning:
		body = a.server.SetSize(a.width, innerH).View()
	case screenExplore:
		body = a.explore.SetSize(a.width, innerH).View()
	case screenExecutor:
		body = a.executor.SetSize(a.width, innerH).View()
	case screenThemeSelector:
		body = a.themeSelector.SetSize(a.width, innerH).View()
	}

	out := toast + body
	if helpView != "" {
		out += "\n" + helpView
	}
	return out
}

func (a *AppModel) helpView() string {
	t := ActiveTheme

	var helpContent string
	switch a.screen {
	case screenHome:
		helpContent = a.help.View(keys.Home)
	case screenModelList:
		helpContent = a.help.View(keys.ModelList)
	case screenProfileBrowser:
		helpContent = a.help.View(keys.ProfileBrowser)
	case screenProfileList:
		helpContent = a.help.View(keys.ProfileList)
	case screenConfirm:
		helpContent = a.help.View(keys.Confirm)
	case screenServerRunning:
		helpContent = a.serverHelpView()
	case screenExplore:
		if a.explore.AddingBrowse() {
			helpContent = a.help.View(keys.FileBrowser)
		} else {
			helpContent = a.help.View(keys.Explore)
		}
	case screenExecutor:
		if a.executor.AddingBrowse() {
			helpContent = a.help.View(keys.FileBrowser)
		} else {
			helpContent = a.help.View(keys.Executor)
		}
	default:
		return ""
	}

	a.help.Styles.ShortKey = lipgloss.NewStyle().Foreground(t.Secondary).Bold(true)
	a.help.Styles.ShortDesc = lipgloss.NewStyle().Foreground(t.Muted)
	a.help.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(t.Muted)
	a.help.Styles.FullKey = lipgloss.NewStyle().Foreground(t.Secondary).Bold(true)
	a.help.Styles.FullDesc = lipgloss.NewStyle().Foreground(t.Muted)
	a.help.Styles.FullSeparator = lipgloss.NewStyle().Foreground(t.Muted)

	return lipgloss.Place(a.width, 1, lipgloss.Center, lipgloss.Center, helpContent)
}

func (a *AppModel) serverHelpView() string {
	if a.showFullHelp {
		if a.server.stopped {
			return a.help.FullHelpView([][]key.Binding{{keys.Server.Restart, keys.Server.Clear}, {keys.Server.Back, keys.Server.Help}})
		}
		return a.help.FullHelpView([][]key.Binding{{keys.Server.Stop, keys.Server.Clear}, {keys.Server.BackStop, keys.Server.Help}})
	}
	if a.server.stopped {
		return a.help.ShortHelpView([]key.Binding{keys.Server.Restart, keys.Server.Clear, keys.Server.Back, keys.Server.Help})
	}
	return a.help.ShortHelpView([]key.Binding{keys.Server.Stop, keys.Server.Clear, keys.Server.BackStop, keys.Server.Help})
}
