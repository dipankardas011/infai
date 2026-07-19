package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/config"
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
)

type toastTickMsg struct{}

type toastKind int

const (
	toastNone toastKind = iota
	toastInfo
	toastSuccess
	toastWarning
	toastError
)

func toastTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return toastTickMsg{} })
}

type screenKind int

const (
	screenHome screenKind = iota
	screenModelList
	screenProfileEdit
	screenServerRunning
	screenThemeSelector
	screenUpdateNotice
)

// Cross-screen messages
type saveProfileMsg struct{ profile model.Profile }
type syncDoneMsg struct {
	removed, updated int
	err              error
}

// AppModel is the root bubbletea model.
type AppModel struct {
	screen       screenKind
	service      *backend.Service
	scanDirs     []string
	width        int
	height       int
	errMsg       string
	errKind      toastKind
	errMsgTicks  int
	quitArmed    bool
	help         help.Model
	showFullHelp bool

	// Sub-models
	home          HomeModel
	modelList     ModelListModel
	profileEdit   ProfileEditModel
	runs          RunsStore
	themeSelector ThemeSelectorModel

	// State tracking
	selectedModel     model.ModelEntry
	profileEditReturn screenKind
	updateLatest      string // newer release tag, set by the async update check
}

func NewApp(database *db.DB, scanDirs []string, entries []model.ModelEntry, w, h int) AppModel {
	service := backend.New(database)
	var startupErrs []string

	data, err := service.LoadHomeData()
	if err != nil {
		startupErrs = append(startupErrs, err.Error())
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

	home := NewHomeModel(service, scanDirs, entries, data.Recents, data.Profiles, w, h)

	// Reopen on the tab the user was last using.
	if v, err := service.GetSetting("last_tab"); err == nil && v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= 0 && idx < len(tabNames) {
			home.activeTab = idx
		}
	}

	return AppModel{
		service:       service,
		scanDirs:      scanDirs,
		width:         w,
		height:        h,
		errMsg:        strings.Join(startupErrs, "; "),
		help:          help.New(),
		home:          home,
		runs:          NewRunsStore(),
		modelList:     NewModelListModel(entries, w, h),
		themeSelector: NewThemeSelectorModel(w, h),
	}
}

func (a *AppModel) Init() tea.Cmd {
	// The update check runs in its own goroutine (Bubble Tea executes each
	// command concurrently); the UI renders immediately and the result
	// arrives later as an updateCheckMsg.
	return tea.Batch(toastTick(), checkForUpdateCmd(config.Version()))
}

func (a *AppModel) setToast(kind toastKind, msg string) {
	a.errMsg = msg
	a.errKind = kind
	a.errMsgTicks = 0
}

func (a *AppModel) setErr(msg string)     { a.setToast(toastError, msg) }
func (a *AppModel) setInfo(msg string)    { a.setToast(toastInfo, msg) }
func (a *AppModel) setSuccess(msg string) { a.setToast(toastSuccess, msg) }
func (a *AppModel) setWarning(msg string) { a.setToast(toastWarning, msg) }

func (a *AppModel) refreshHome() {
	data, err := a.service.LoadHomeData()
	if err != nil {
		a.setErr(err.Error())
	}
	a.scanDirs = data.ScanDirs
	a.modelList = a.modelList.SetEntries(data.Models)
	a.home = a.home.RefreshProfiles(data.Recents, data.Profiles)
	a.home = a.home.RefreshModels(a.scanDirs)
	a.home = a.home.RefreshEngines()
	a.syncRunsToHome()
}

func (a *AppModel) syncRunsToHome() {
	a.home = a.home.SetRuns(a.runs.Snapshot())
}

func (a *AppModel) launchRun(m model.ModelEntry, p model.Profile, engine model.InferenceEngine, openDetail bool) tea.Cmd {
	if existingID, ok := a.runs.ActiveProfileRun(p.ID); ok {
		a.runs.SetActive(existingID)
		a.syncRunsToHome()
		a.home = a.home.SelectRun(existingID)
		a.home.activeTab = tabRuns
		a.screen = screenHome
		a.setInfo(fmt.Sprintf("%s is already running as run #%d", p.Name, existingID))
		return nil
	}
	if engine.ID == "" && p.InferenceEngineID != "" {
		var err error
		engine, err = a.service.GetInferenceEngineByID(p.InferenceEngineID)
		if err != nil {
			a.setErr("inference engine: " + err.Error())
			return nil
		}
	}
	actualPort, err := pickRunPort(p.Host, p.Port, a.runs.OccupiedPorts())
	if err != nil {
		a.setErr(err.Error())
		return nil
	}
	spec, err := a.service.BuildRunSpec(m, p, actualPort)
	if err != nil {
		a.setErr(err.Error())
		return nil
	}
	runID := a.runs.NewID()
	sm, cmd, err := NewServerModel(runID, spec, p.Name, m.DisplayName, m.Type, p.ContextSize, p.Host, actualPort, a.width, a.height)
	if err != nil {
		a.setErr(err.Error())
		a.refreshHome()
		return nil
	}
	a.runs.Add(RunRecord{
		ID: runID, ProfileID: p.ID, ModelID: m.ID,
		ProfileName: p.Name, ModelName: m.DisplayName, ModelType: m.Type,
		EngineID: engine.ID, EngineName: engine.Name, EnginePath: engine.Path,
		Host: p.Host, RequestedPort: p.Port, ActualPort: actualPort,
		Model: m, Profile: p,
		Server: sm,
	})
	_ = a.service.MarkRecent(m.ID, p.ID)
	a.refreshHome()
	if actualPort != p.Port {
		a.setWarning(fmt.Sprintf("port %d busy; launched %s as run #%d on :%d", p.Port, p.Name, runID, actualPort))
	} else {
		a.setSuccess(fmt.Sprintf("launched %s as run #%d on :%d — enter opens details", p.Name, runID, actualPort))
	}
	if openDetail {
		a.screen = screenServerRunning
	} else {
		a.home.activeTab = tabRuns
		a.screen = screenHome
	}
	return cmd
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
				a.errKind = toastNone
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
		a.runs.SetSize(a.width, a.height)
		a.themeSelector = a.themeSelector.SetSize(a.width, a.height)
		return a, nil

	case updateCheckMsg:
		if msg.latest == "" {
			return a, nil // current, dev build, or check failed — stay quiet
		}
		a.updateLatest = msg.latest
		if a.screen == screenHome {
			a.screen = screenUpdateNotice
		} else {
			// Don't stomp another screen; a toast is enough.
			a.setWarning(updateToastText(msg.latest))
		}
		return a, nil

	case profilesTabLaunchMsg:
		cmd := a.launchRun(msg.entry.Model, msg.entry.Profile, msg.entry.InferenceEngine, false)
		return a, cmd

	case profilesTabNewProfileMsg:
		return a.openProfileModelPicker(screenHome)

	case profilesTabEditProfileMsg:
		profile, err := a.service.GetProfile(msg.entry.Profile.ID)
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		engines, err := a.service.ListInferenceEngines()
		if err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.selectedModel = msg.entry.Model
		a.profileEdit = NewProfileEditModel(msg.entry.Model, engines, &profile, a.width, a.height)
		a.profileEditReturn = screenHome
		a.screen = screenProfileEdit
		a.errMsg = ""
		return a, nil

	case profilesTabDeleteProfileMsg:
		if a.runs.HasActiveProfile(msg.id) {
			a.setWarning("stop active runs before deleting this profile")
			return a, nil
		}
		if err := a.service.DeleteProfile(msg.id); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.refreshHome()
		return a, nil

	case enginesTabChangedMsg:
		a.refreshHome()
		return a, nil

	case modelsTabChangedMsg:
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

	case saveProfileMsg:
		p := msg.profile
		if err := a.service.SaveProfile(&p); err != nil {
			a.setErr(err.Error())
			return a, nil
		}
		a.refreshHome()
		a.screen = screenHome
		return a, nil

	// Run log/process streaming is global so background runs keep updating.
	case logLineMsg:
		if run, ok := a.runs.Get(msg.runID); ok {
			var cmd tea.Cmd
			run.Server, cmd = run.Server.HandleLogLine(msg.line)
			a.syncRunsToHome()
			return a, cmd
		}
		return a, nil

	case serverExitMsg:
		if run, ok := a.runs.Get(msg.runID); ok {
			run.Server = run.Server.SetExited(msg.err)
			a.syncRunsToHome()
		}
		return a, nil

	case stopTimeoutMsg:
		if run, ok := a.runs.Get(msg.runID); ok && !run.Server.stopped && run.Server.stopping {
			run.Server = run.Server.ForceKill()
			a.setErr("run unresponsive — sent SIGKILL")
			a.syncRunsToHome()
		}
		return a, nil

	case systemMetricsMsg, tickMetricsMsg, engineMetricsMsg:
		return a.updateRunMessage(msg)

	case runsTabOpenMsg:
		if a.runs.SetActive(msg.id) {
			a.screen = screenServerRunning
		}
		return a, nil

	case runsTabStopMsg:
		if run, ok := a.runs.Get(msg.id); ok {
			var cmd tea.Cmd
			run.Server, cmd = run.Server.Stop()
			a.syncRunsToHome()
			return a, cmd
		}
		return a, nil

	case runsTabRestartMsg:
		return a.restartRun(msg.id)

	case runsTabRemoveMsg:
		if !a.runs.RemoveStopped(msg.id) {
			a.setWarning("stop the run before removing it")
		} else {
			a.setSuccess("removed stopped run")
			a.syncRunsToHome()
		}
		return a, nil

	case tea.KeyMsg:
		// Global keys
		if msg.String() == "ctrl+c" {
			if a.quitArmed {
				a.forceKillAllRuns()
				return a, tea.Quit
			}
			if a.runs.ActiveCount() > 0 {
				a.quitArmed = true
				a.setWarning("shutting down active runs... ctrl+c again to force quit")
				return a, a.stopAllRuns()
			}
			a.quitArmed = true
			a.setWarning("press ctrl+c again to quit")
			return a, nil
		}

		// While the user is typing (list filters, name inputs, file browser) or a
		// modal dialog is open, printable keys belong to that input — never treat
		// them as global shortcuts.
		typing := (a.screen == screenHome && a.home.InModalInput()) ||
			(a.screen == screenModelList && a.modelList.IsFiltering())

		// Global 'q' quit on home. With active runs, first q sends SIGTERM;
		// second q force-kills and exits.
		if a.screen == screenHome && !typing && msg.String() == "q" {
			if a.quitArmed {
				a.forceKillAllRuns()
				return a, tea.Quit
			}
			if a.runs.ActiveCount() > 0 {
				a.quitArmed = true
				a.setWarning("shutting down active runs... q again to force quit")
				return a, a.stopAllRuns()
			}
			return a, tea.Quit
		}

		if a.quitArmed {
			a.quitArmed = false
			a.errMsg = ""
			a.errKind = toastNone
		}

		// The update notice is modal: any of enter/esc/q/space dismisses it.
		if a.screen == screenUpdateNotice {
			switch msg.String() {
			case "enter", "esc", "q", " ":
				a.screen = screenHome
			}
			return a, nil
		}

		// Theme key works everywhere except editor/server
		if msg.String() == "t" && !typing && a.screen != screenProfileEdit && a.screen != screenServerRunning {
			a.themeSelector = NewThemeSelectorModel(a.width, a.height)
			a.screen = screenThemeSelector
			return a, nil
		}

		// Help toggle
		if msg.String() == "?" && !typing && a.screen != screenProfileEdit {
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
		case screenServerRunning:
			return a.updateServer(msg)
		case screenThemeSelector:
			return a.updateThemeSelector(msg)
		}
	}

	// Non-key messages: delegate to active sub-model
	return a.handleNonKeyMsg(msg)
}

func (a *AppModel) restartRun(id RunID) (tea.Model, tea.Cmd) {
	run, ok := a.runs.Get(id)
	if !ok {
		return a, nil
	}
	if !run.Server.stopped || run.Server.stopping {
		a.setWarning("only stopped runs can be restarted")
		return a, nil
	}
	actualPort, err := pickRunPort(run.Host, run.RequestedPort, a.runs.OccupiedPorts())
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	spec, err := a.service.BuildRunSpec(run.Model, run.Profile, actualPort)
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	nextServer := run.Server
	nextServer.runSpec = spec
	nextServer.port = actualPort
	sm, cmd, err := nextServer.Restart()
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	run.ActualPort = actualPort
	run.Server = sm
	a.runs.SetActive(id)
	a.syncRunsToHome()
	a.setSuccess(fmt.Sprintf("restarted %s on :%d", run.ProfileName, actualPort))
	return a, cmd
}

func (a *AppModel) stopAllRuns() tea.Cmd {
	cmds := make([]tea.Cmd, 0)
	for _, snap := range a.runs.Snapshot() {
		run, ok := a.runs.Get(snap.ID)
		if !ok || run.Server.stopped || run.Server.stopping {
			continue
		}
		var cmd tea.Cmd
		run.Server, cmd = run.Server.Stop()
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	a.syncRunsToHome()
	return tea.Batch(cmds...)
}

func (a *AppModel) forceKillAllRuns() {
	for _, snap := range a.runs.Snapshot() {
		if run, ok := a.runs.Get(snap.ID); ok && !run.Server.stopped {
			run.Server = run.Server.ForceKill()
		}
	}
	a.syncRunsToHome()
}

func (a *AppModel) updateRunMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	var id RunID
	switch m := msg.(type) {
	case systemMetricsMsg:
		id = m.runID
	case tickMetricsMsg:
		id = m.runID
	case engineMetricsMsg:
		id = m.runID
	default:
		return a, nil
	}
	run, ok := a.runs.Get(id)
	if !ok {
		return a, nil
	}
	var cmd tea.Cmd
	run.Server, cmd = run.Server.Update(msg)
	a.syncRunsToHome()
	return a, cmd
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
	case screenServerRunning:
		if run, ok := a.runs.Active(); ok {
			var cmd tea.Cmd
			run.Server, cmd = run.Server.Update(msg)
			a.syncRunsToHome()
			return a, cmd
		}
		a.screen = screenHome
		return a, nil
	case screenThemeSelector:
		var cmd tea.Cmd
		a.themeSelector, cmd = a.themeSelector.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *AppModel) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prevTab := a.home.activeTab
	var cmd tea.Cmd
	a.home, cmd = a.home.Update(msg)
	if a.home.activeTab != prevTab {
		// Remember the tab across sessions, like the theme.
		a.service.SetSetting("last_tab", strconv.Itoa(a.home.activeTab))
	}
	return a, cmd
}

func (a *AppModel) updateThemeSelector(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.themeSelector.Revert() // undo the live preview
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
			a.refreshHome()
			a.screen = screenHome
			return a, nil
		}
	case "enter":
		if a.modelList.IsFiltering() {
			break // enter accepts the filter; let the list handle it
		}
		if entry, ok := a.modelList.Selected(); ok {
			a.selectedModel = entry
			engines, err := a.service.ListInferenceEngines()
			if err != nil {
				a.setErr(err.Error())
				return a, nil
			}
			if len(engines) == 0 {
				a.setErr("no inference engines configured - add one in Engines tab")
				a.screen = screenHome
				return a, nil
			}
			a.profileEdit = NewProfileEditModel(entry, engines, nil, a.width, a.height)
			a.profileEditReturn = screenHome
			a.screen = screenProfileEdit
			a.errMsg = ""
			return a, nil
		}
	case "r":
		if a.modelList.IsFiltering() {
			break // 'r' is filter text, not the rescan shortcut
		}
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
	engines, err := a.service.ListInferenceEngines()
	if err != nil {
		a.setErr(err.Error())
		return a, nil
	}
	if len(engines) == 0 {
		a.setErr("no inference engines configured - add one in Engines tab")
		return a, nil
	}
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
	a.profileEditReturn = returnScreen
	a.screen = screenModelList
	return a, nil
}

func (a *AppModel) updateProfileEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// "Discard changes?" dialog captures all keys until answered.
	if a.profileEdit.discardConfirm {
		switch msg.String() {
		case "y":
			a.profileEdit.discardConfirm = false
			a.profileEdit.errMsg = ""
			a.screen = a.profileEditReturn
		case "n", "esc":
			a.profileEdit.discardConfirm = false
		}
		return a, nil
	}
	switch msg.String() {
	case "esc":
		if a.profileEdit.dirty() {
			a.profileEdit.discardConfirm = true
			return a, nil
		}
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

func (a *AppModel) updateServer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	run, ok := a.runs.Active()
	if !ok {
		a.screen = screenHome
		return a, nil
	}
	switch msg.String() {
	case "s":
		if run.Server.stopped || run.Server.stopping {
			return a, nil
		}
		var cmd tea.Cmd
		run.Server, cmd = run.Server.Stop()
		a.syncRunsToHome()
		return a, cmd
	case "r":
		return a.restartRun(run.ID)
	case "[":
		a.runs.NextActive(-1)
		return a, nil
	case "]":
		a.runs.NextActive(1)
		return a, nil
	case "y":
		logText := strings.Join(run.Server.logs, "\n")
		if strings.TrimSpace(logText) == "" {
			a.setInfo("no log lines to copy yet")
			return a, nil
		}
		bin, err := CopyToClipboard(logText)
		if err != nil {
			a.setErr("clipboard copy failed: " + err.Error())
		} else if bin != "" {
			a.setSuccess("copied log lines to clipboard via " + bin)
		} else {
			a.setWarning("no clipboard tool found (install wl-copy, xclip, or pbcopy)")
		}
		return a, nil
	case "esc":
		a.refreshHome()
		a.home.activeTab = tabRuns
		a.screen = screenHome
		return a, nil
	}
	var cmd tea.Cmd
	run.Server, cmd = run.Server.Update(msg)
	a.syncRunsToHome()
	return a, cmd
}

func (a *AppModel) renderToast() string {
	switch a.errKind {
	case toastSuccess:
		return styleSuccess.Render("✓ " + a.errMsg)
	case toastWarning:
		return styleWarning.Bold(true).Render("! " + a.errMsg)
	case toastInfo:
		return styleMuted.Render("• " + a.errMsg)
	case toastError:
		fallthrough
	default:
		return styleError.Render("✗ " + a.errMsg)
	}
}

func (a *AppModel) View() string {
	// Minimum size warning
	if a.width < MinWindowWidth || a.height < MinWindowHeight {
		return RenderMinSizeWarning(a.width, a.height)
	}

	// The status line is always reserved so toasts never shift the layout.
	toast := ""
	if a.errMsg != "" {
		toast = a.renderToast()
	}
	toastLines := 1

	helpView := a.helpView()
	helpLines := 0
	if helpView != "" {
		helpLines = 1
		if a.showFullHelp {
			helpLines = 3
		}
	}

	// Global header is shown on every screen.
	headerView := RenderHeader(a.width, a.runs.ActiveCount())
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
	case screenServerRunning:
		if run, ok := a.runs.Active(); ok {
			run.Server = run.Server.SetSize(a.width, innerH)
			body = run.Server.View()
		} else {
			body = styleMuted.Render("no run selected")
		}
	case screenThemeSelector:
		body = a.themeSelector.SetSize(a.width, innerH).View()
	case screenUpdateNotice:
		body = renderUpdateNotice(config.Version(), a.updateLatest, a.width, innerH)
	}

	// Final safety: active screen must not exceed the space reserved for it.
	// This preserves fixed header/footer regions in all screens.
	body = ClampHeight(contentArea, body)

	out := headerView + "\n" + toast + "\n" + body
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
		case tabRuns:
			helpContent = a.help.View(keys.Runs)
		case tabModels:
			helpContent = a.help.View(keys.Models)
		case tabEngines:
			helpContent = a.help.View(keys.Engines)
		}
	case screenModelList:
		helpContent = a.help.View(keys.ModelList)
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
	run, ok := a.runs.Active()
	if !ok {
		return a.help.ShortHelpView([]key.Binding{keys.Server.Back, keys.Server.Help})
	}
	switchRun := key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[/]", "switch run"))
	if a.showFullHelp {
		if run.Server.stopped {
			return a.help.FullHelpView([][]key.Binding{{keys.Server.Restart, keys.Server.Clear, keys.Server.Copy}, {switchRun, keys.Server.Back, keys.Server.Help}})
		}
		return a.help.FullHelpView([][]key.Binding{{keys.Server.Stop, keys.Server.Clear, keys.Server.Copy}, {switchRun, keys.Server.Back, keys.Server.Help}})
	}
	if run.Server.stopped {
		return a.help.ShortHelpView([]key.Binding{keys.Server.Restart, keys.Server.Clear, keys.Server.Copy, switchRun, keys.Server.Back, keys.Server.Help})
	}
	return a.help.ShortHelpView([]key.Binding{keys.Server.Stop, keys.Server.Clear, keys.Server.Copy, switchRun, keys.Server.Back, keys.Server.Help})
}
