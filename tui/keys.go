package tui

import "github.com/charmbracelet/bubbles/key"

// ── Home (tabbed) ──────────────────────────────────────────────────────────
type homeKeyMap struct {
	TabLeft  key.Binding
	TabRight key.Binding
	Quit     key.Binding
	Theme    key.Binding
	Help     key.Binding
}

func (k homeKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.TabLeft, k.TabRight, k.Theme, k.Quit, k.Help}
}
func (k homeKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.TabLeft, k.TabRight, k.Theme, k.Quit, k.Help}}
}

// ── Model List ─────────────────────────────────────────────────────────────
type modelListKeyMap struct {
	Enter  key.Binding
	Rescan key.Binding
	Filter key.Binding
	Back   key.Binding
	Help   key.Binding
}

func (k modelListKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Rescan, k.Filter, k.Back, k.Help}
}
func (k modelListKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Enter, k.Rescan}, {k.Filter, k.Back}}
}

// ── Server (logs) ──────────────────────────────────────────────────────────
type serverKeyMap struct {
	Stop     key.Binding
	Restart  key.Binding
	Clear    key.Binding
	Copy     key.Binding
	Back     key.Binding
	BackStop key.Binding
	Help     key.Binding
}

func (k serverKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Stop, k.Clear, k.Copy, k.BackStop, k.Help}
}
func (k serverKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// ── Theme ──────────────────────────────────────────────────────────────────
type themeKeyMap struct {
	Select key.Binding
	Back   key.Binding
}

func (k themeKeyMap) ShortHelp() []key.Binding  { return []key.Binding{k.Select, k.Back} }
func (k themeKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type profileTabKeyMap struct {
	Launch   key.Binding
	New      key.Binding
	Edit     key.Binding
	Delete   key.Binding
	Filter   key.Binding
	TabLeft  key.Binding
	TabRight key.Binding
	Theme    key.Binding
	Quit     key.Binding
	Help     key.Binding
}

func (k profileTabKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Launch, k.New, k.Edit, k.Delete, k.Filter, k.TabLeft, k.TabRight, k.Help}
}
func (k profileTabKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Launch, k.New, k.Edit, k.Delete}, {k.Filter, k.TabLeft, k.TabRight, k.Theme, k.Quit}}
}

type runsTabKeyMap struct {
	Open     key.Binding
	Stop     key.Binding
	Restart  key.Binding
	Remove   key.Binding
	Page     key.Binding
	TabLeft  key.Binding
	TabRight key.Binding
	Theme    key.Binding
	Quit     key.Binding
	Help     key.Binding
}

func (k runsTabKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Open, k.Stop, k.Restart, k.Remove, k.Page, k.TabLeft, k.TabRight, k.Help}
}
func (k runsTabKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Open, k.Stop, k.Restart, k.Remove, k.Page}, {k.TabLeft, k.TabRight, k.Theme, k.Quit}}
}

type modelsTabKeyMap struct {
	Info     key.Binding
	Import   key.Binding
	Download key.Binding
	Remove   key.Binding
	Sync     key.Binding
	OpenHF   key.Binding
	TabLeft  key.Binding
	TabRight key.Binding
	Theme    key.Binding
	Quit     key.Binding
	Help     key.Binding
}

func (k modelsTabKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Info, k.Import, k.Download, k.Remove, k.Sync, k.OpenHF, k.TabLeft, k.TabRight, k.Help}
}
func (k modelsTabKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Info, k.Import, k.Download, k.Remove, k.Sync, k.OpenHF}, {k.TabLeft, k.TabRight, k.Theme, k.Quit}}
}

type fileBrowserKeyMap struct {
	Select key.Binding
	Open   key.Binding
	Back   key.Binding
	Filter key.Binding
	Home   key.Binding
	Cancel key.Binding
}

func (k fileBrowserKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Select, k.Open, k.Back, k.Filter, k.Home, k.Cancel}
}
func (k fileBrowserKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Select, k.Open, k.Back}, {k.Filter, k.Home, k.Cancel}}
}

type enginesTabKeyMap struct {
	Add      key.Binding
	Rename   key.Binding
	Delete   key.Binding
	TabLeft  key.Binding
	TabRight key.Binding
	Theme    key.Binding
	Quit     key.Binding
	Help     key.Binding
}

func (k enginesTabKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Add, k.Rename, k.Delete, k.TabLeft, k.TabRight, k.Help}
}
func (k enginesTabKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Add, k.Rename, k.Delete}, {k.TabLeft, k.TabRight, k.Theme, k.Quit}}
}

var keys = struct {
	Home        homeKeyMap
	Profiles    profileTabKeyMap
	Runs        runsTabKeyMap
	Models      modelsTabKeyMap
	Engines     enginesTabKeyMap
	ModelList   modelListKeyMap
	FileBrowser fileBrowserKeyMap
	Server      serverKeyMap
	Theme     themeKeyMap
}{
	Home: homeKeyMap{
		TabLeft:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("s-tab", "prev tab")),
		TabRight: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	ModelList: modelListKeyMap{
		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Rescan: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rescan")),
		Filter: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Server: serverKeyMap{
		Stop:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
		Restart:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Clear:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear logs")),
		Copy:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy logs")),
		Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		BackStop: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Profiles: profileTabKeyMap{
		Launch:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "launch")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Edit:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		TabLeft:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("s-tab", "prev tab")),
		TabRight: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Runs: runsTabKeyMap{
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "view")),
		Stop:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
		Restart:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Remove:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
		Page:     key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pg", "page")),
		TabLeft:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("s-tab", "prev tab")),
		TabRight: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Models: modelsTabKeyMap{
		Info:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "info")),
		Import:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "import")),
		Download: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "download")),
		Remove:   key.NewBinding(key.WithKeys("x", "d", "delete"), key.WithHelp("x", "remove")),
		Sync:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
		OpenHF:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open on HF")),
		TabLeft:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("s-tab", "prev tab")),
		TabRight: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Engines: enginesTabKeyMap{
		Add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add engine")),
		Rename:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "rename")),
		Delete:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")),
		TabLeft:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("s-tab", "prev tab")),
		TabRight: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	FileBrowser: fileBrowserKeyMap{
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Open:   key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "open")),
		Back:   key.NewBinding(key.WithKeys("left", "backspace", "b"), key.WithHelp("←", "back")),
		Filter: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Home:   key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "~")),
		Cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	},
	Theme: themeKeyMap{
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	},
}
