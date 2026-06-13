package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
)

type toastTickMsg struct{}

func toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastTickMsg{} })
}

type screenKind int

const (
	screenHome screenKind = iota
	screenModelList
	screenProfileEdit
	screenConfirm
	screenServerRunning
	screenThemeSelector
)

type modelListPurpose int

const (
	modelListBrowse modelListPurpose = iota
	modelListPickForProfile
)

// Cross-screen messages
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
	service      *backend.Service
	serverBin    string
	scanDirs     []string
	width        int
	height       int
	errMsg       string
	errMsgTicks  int
	quitArmed    bool
	help         help.Model
	showFullHelp bool

	// Sub-models
	home          HomeModel
	modelList     ModelListModel
	profileEdit   ProfileEditModel
	confirm       ConfirmModel
	server        ServerModel
	themeSelector ThemeSelectorModel

	// State tracking
	selectedModel     model.ModelEntry
	selectedProfile   model.Profile
	modelListPurpose  modelListPurpose
	profileEditReturn screenKind
	confirmReturn     screenKind
}

func NewApp(database *db.DB, serverBin string, scanDirs []string, entries []model.ModelEntry, w, h int) AppModel {
	service := backend.New(database)
	var startupErrs []string

	data, err := service.LoadHomeData(serverBin)
	if err != nil {
		startupErrs = append(startupErrs, err.Error())
	}
	if data.ServerBin != "" {
		serverBin = data.ServerBin
	}
	if len(data.ScanDirs) > 0 {
		scanDirs = data.ScanDirs
	}
	if len(data.Models) > 0 {
		entries = data.Models
	}

	// Load theme from settings
	if themeName, err := service.GetSetting("theme"); err == nil && themeName != "" {
		SetTheme(themeName)
	}

	home := NewHomeModel(service, serverBin, scanDirs, entries, data.Recents, data.Profiles, w, h)

	return AppModel{
		service:       service,
		serverBin:     serverBin,
		scanDirs:      scanDirs,
		width:         w,
		height:        h,
		errMsg:        strings.Join(startupErrs, "; "),
		help:          help.New(),
		home:          home,
		modelList:     NewModelListModel(entries, w, h),
		themeSelector: NewThemeSelectorModel(w, h),
	}
}

func (a *AppModel) Init() tea.Cmd { return toastTick() }

func (a *AppModel) setErr(msg string) {
	a.errMsg = msg
	a.errMsgTicks = 0
}

func (a *AppModel) refreshHome() {
	data, err := a.service.LoadHomeData(a.serverBin)
	if err != nil {
		a.setErr(err.Error())
	}
	if data.ServerBin != "" {
		a.serverBin = data.ServerBin
	}
	a.scanDirs = data.ScanDirs
	a.modelList = a.modelList.SetEntries(data.Models)
	a.home = a.home.RefreshProfiles(data.Recents, data.Profiles)
	a.home = a.home.RefreshModels(a.scanDirs)
	a.home = a.home.RefreshEngines(a.serverBin)
	// Sync serverBin from engines tab (may have been updated in UI state).
	if effective := a.home.EffectiveServerBin(); effective != "" {
		a.serverBin = effective
	}
}

func (a *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Check minimum window size on the main home screen
	if a.screen == screenHome {
		if w, ok := msg.(tea.WindowSizeMsg); ok {
			if w.Width < MinWindowWidth || w.Height < MinWindowHeight {
				return a, nil
			}
		}
		if a.width > 0 && a.height > 0 &&
			(a.width < MinWindowWidth || a.height < MinWindowHeight) {
			return a, nil
		}
	}

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
		a.profileEdit = a.profileEdit.SetSize(a.width, a.height)
		a.confirm = a.confirm.SetSize(a.width, a.height)
		a.server = a.server.SetSize(a.width, a.height)
		a.themeSelector = a.themeSelector.SetSize(a.width, a.height)
		return a, nil

	case profilesTabLaunchMsg:
		a.selectedModel = msg.entry.Model
		a.selectedProfile = msg.entry.Profile
		// Launch directly — no confirm screen
		args, err := a.service.BuildLaunchArgs(a.serverBin, msg.entry.Model, msg.entry.Profile)
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		_ = a.service.MarkRecent(a.selectedModel.ID, a.selectedProfile.ID)
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
			a.refreshHome()
			return a, nil
		}
		a.server = sm
		a.screen = screenServerRunning
		return a, cmd

	case profilesTabNewProfileMsg:
		return a.openProfileModelPicker(screenHome)

	case profilesTabEditProfileMsg:
		profile, err := a.service.GetProfile(msg.entry.Profile.ID)
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.selectedModel = msg.entry.Model
		a.profileEdit = NewProfileEditModel(msg.entry.Model, &profile, a.width, a.height)
		a.profileEditReturn = screenHome
		a.screen = screenProfileEdit
		a.errMsg = ""
		return a, nil

	case profilesTabDeleteProfileMsg:
		if err := a.service.DeleteProfile(msg.id); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.refreshHome()
		return a, nil

	case modelsTabSyncDoneMsg:
		if msg.err != nil {
			a.setErr(msg.err.Error())
			return a, nil
		}
		entries, _ := a.service.ListModels()
		a.modelList = a.modelList.SetEntries(entries)
		a.refreshHome()
		return a, nil

	case syncDoneMsg:
		if msg.err != nil {
			a.setErr(msg.err.Error())
			return a, nil
		}
		entries, _ := a.service.ListModels()
		a.modelList = a.modelList.SetEntries(entries)
		a.refreshHome()
		return a, nil

	case scanDoneMsg:
		// Legacy message kept for compatibility with older screens. New scan/sync
		// workflows should go through backend.Service.
		if msg.err != nil {
			a.setErr(msg.err.Error())
			return a, nil
		}
		a.refreshHome()
		return a, nil

	case saveProfileMsg:
		p := msg.profile
		if err := a.service.SaveProfile(&p); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.refreshHome()
		a.screen = screenHome
		return a, nil

	case deleteProfileMsg:
		if err := a.service.DeleteProfile(msg.id); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.refreshHome()
		return a, nil

	// Server log streaming
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
		// Global keys
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

		// Global 'q' quit on home
		if a.screen == screenHome && msg.String() == "q" {
			return a, tea.Quit
		}

		// Theme key works everywhere except editor/server
		if msg.String() == "t" && a.screen != screenProfileEdit && a.screen != screenServerRunning {
			a.themeSelector = NewThemeSelectorModel(a.width, a.height)
			a.screen = screenThemeSelector
			return a, nil
		}

		// Help toggle
		if msg.String() == "?" && a.screen != screenProfileEdit {
			a.showFullHelp = !a.showFullHelp
			a.help.ShowAll = a.showFullHelp
			return a, nil
		}

		// Dispatch to active screen
		switch a.screen {
		case screenHome:
			return a.updateHome(msg)
		case screenModelList:
			return a.updateModelList(msg)
		case screenProfileEdit:
			return a.updateProfileEdit(msg)
		case screenConfirm:
			return a.updateConfirm(msg)
		case screenServerRunning:
			return a.updateServer(msg)
		case screenThemeSelector:
			return a.updateThemeSelector(msg)
		}
	}

	// Non-key messages: delegate to active sub-model
	return a.handleNonKeyMsg(msg)
}

func (a *AppModel) handleNonKeyMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.screen {
	case screenHome:
		var cmd tea.Cmd
		a.home, cmd = a.home.Update(msg)
		return a, cmd
	case screenModelList:
		var cmd tea.Cmd
		a.modelList, cmd = a.modelList.Update(msg)
		return a, cmd
	case screenProfileEdit:
		var cmd tea.Cmd
		a.profileEdit, cmd = a.profileEdit.Update(msg)
		return a, cmd
	case screenConfirm:
		var cmd tea.Cmd
		a.confirm, cmd = a.confirm.Update(msg)
		return a, cmd
	case screenServerRunning:
		var cmd tea.Cmd
		a.server, cmd = a.server.Update(msg)
		return a, cmd
	case screenThemeSelector:
		var cmd tea.Cmd
		a.themeSelector, cmd = a.themeSelector.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *AppModel) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		a.service.SetSetting("theme", theme.Name)
		a.screen = screenHome
		return a, nil
	}
	var cmd tea.Cmd
	a.themeSelector, cmd = a.themeSelector.Update(msg)
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
				a.refreshHome()
				a.screen = screenHome
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
				a.profileEditReturn = screenHome
				a.screen = screenProfileEdit
				a.errMsg = ""
				return a, nil
			}
			// Browsing model list directly
		}
	case "r":
		service := a.service
		folders := append([]string(nil), a.scanDirs...)
		return a, func() tea.Msg {
			res, err := service.SyncModels(folders)
			return syncDoneMsg{removed: res.Removed, updated: res.Updated, err: err}
		}
	}
	var cmd tea.Cmd
	a.modelList, cmd = a.modelList.Update(msg)
	return a, cmd
}

func (a *AppModel) openProfileModelPicker(returnScreen screenKind) (tea.Model, tea.Cmd) {
	entries, err := a.service.ListModels()
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	if len(entries) == 0 {
		a.setErr("no models found - add scan folders in Models tab")
		return a, nil
	}
	a.modelList = NewModelListModel(entries, a.width, a.height).SetTitle("Choose model for new profile")
	a.modelListPurpose = modelListPickForProfile
	a.profileEditReturn = returnScreen
	a.screen = screenModelList
	return a, nil
}

func (a *AppModel) updateProfileEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.screen = a.profileEditReturn
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
		if a.confirmReturn == screenHome {
			a.refreshHome()
		}
		a.screen = screenHome
		return a, nil
	case "enter":
		if a.confirm.command == "" {
			a.setErr("no executor configured - set one in Engines tab")
			a.screen = screenHome
			return a, nil
		}
		args := a.confirm.Args()
		if len(args) == 0 || args[0] == "" {
			a.setErr("executor path not set - set one in Engines tab")
			a.screen = screenHome
			return a, nil
		}
		_ = a.service.MarkRecent(a.selectedModel.ID, a.selectedProfile.ID)
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
	case "y":
		// Copy logs to clipboard
		logText := strings.Join(a.server.logs, "\n")
		bin, err := CopyToClipboard(logText)
		if err != nil {
			a.setErr("clipboard copy failed: " + err.Error())
		} else if bin != "" {
			a.setErr("logs copied to clipboard via " + bin)
		} else {
			a.setErr("no clipboard tool found (install wl-copy, xclip, or pbcopy)")
		}
		return a, nil
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
	// Pass scrolling keys to viewport
	var cmd tea.Cmd
	a.server, cmd = a.server.Update(msg)
	return a, cmd
}

func (a *AppModel) View() string {
	// Minimum size warning
	if a.width < MinWindowWidth || a.height < MinWindowHeight {
		return RenderMinSizeWarning(a.width, a.height)
	}

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

	// Global header is shown on every screen.
	headerView := RenderHeader(a.width)
	headerLines := 1

	// Account for the "\n" separator before helpView.
	sepLines := 0
	if helpView != "" {
		sepLines = 1
	}
	reservedH := headerLines + toastLines + helpLines + sepLines
	contentArea := NewArea(a.width, a.height).ReserveHeight(reservedH)
	innerH := max(contentArea.H, 1)

	var body string
	switch a.screen {
	case screenHome:
		body = a.home.SetSize(a.width, innerH).View()
	case screenModelList:
		body = a.modelList.SetSize(a.width, innerH).View()
	case screenProfileEdit:
		body = a.profileEdit.SetSize(a.width, innerH).View()
	case screenConfirm:
		body = a.confirm.SetSize(a.width, innerH).View()
	case screenServerRunning:
		body = a.server.SetSize(a.width, innerH).View()
	case screenThemeSelector:
		body = a.themeSelector.SetSize(a.width, innerH).View()
	}

	// Final safety: active screen must not exceed the space reserved for it.
	// This preserves fixed header/footer regions in all screens.
	body = ClampHeight(contentArea, body)

	out := headerView + "\n" + toast + body
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
		switch a.home.activeTab {
		case tabProfiles:
			helpContent = a.help.View(keys.Profiles)
		case tabModels:
			helpContent = a.help.View(keys.Models)
		case tabEngines:
			helpContent = a.help.View(keys.Engines)
		}
	case screenModelList:
		helpContent = a.help.View(keys.ModelList)
	case screenConfirm:
		helpContent = a.help.View(keys.Confirm)
	case screenServerRunning:
		helpContent = a.serverHelpView()
	case screenThemeSelector:
		helpContent = a.help.View(keys.Theme)
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
			return a.help.FullHelpView([][]key.Binding{{keys.Server.Restart, keys.Server.Clear, keys.Server.Copy}, {keys.Server.Back, keys.Server.Help}})
		}
		return a.help.FullHelpView([][]key.Binding{{keys.Server.Stop, keys.Server.Clear, keys.Server.Copy}, {keys.Server.BackStop, keys.Server.Help}})
	}
	if a.server.stopped {
		return a.help.ShortHelpView([]key.Binding{keys.Server.Restart, keys.Server.Clear, keys.Server.Copy, keys.Server.Back, keys.Server.Help})
	}
	return a.help.ShortHelpView([]key.Binding{keys.Server.Stop, keys.Server.Clear, keys.Server.Copy, keys.Server.BackStop, keys.Server.Help})
}
