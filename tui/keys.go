package tui

import "github.com/charmbracelet/bubbles/key"

type homeKeyMap struct {
	All       key.Binding
	Folders   key.Binding
	Configure key.Binding
	Theme     key.Binding
	Enter     key.Binding
	Quit      key.Binding
	Help      key.Binding
}

func (k homeKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.All, k.Folders, k.Configure, k.Theme, k.Quit, k.Help}
}
func (k homeKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Enter, k.All, k.Folders},
		{k.Configure, k.Theme, k.Quit},
	}
}

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

type exploreKeyMap struct {
	Add  key.Binding
	Del  key.Binding
	Sync key.Binding
	Back key.Binding
	Help key.Binding
}

func (k exploreKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Add, k.Del, k.Sync, k.Back, k.Help}
}
func (k exploreKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type fileBrowserKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Back   key.Binding
	Open   key.Binding
	Home   key.Binding
	Select key.Binding
}

func (k fileBrowserKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Back, k.Open, k.Home, k.Select}
}
func (k fileBrowserKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type profileListKeyMap struct {
	Enter  key.Binding
	Edit   key.Binding
	Delete key.Binding
	Back   key.Binding
	Help   key.Binding
}

func (k profileListKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Edit, k.Delete, k.Back, k.Help}
}
func (k profileListKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type confirmKeyMap struct {
	Launch key.Binding
	Back   key.Binding
	Help   key.Binding
}

func (k confirmKeyMap) ShortHelp() []key.Binding  { return []key.Binding{k.Launch, k.Back, k.Help} }
func (k confirmKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type serverKeyMap struct {
	Stop  key.Binding
	Clear key.Binding
	Back  key.Binding
	Help  key.Binding
}

func (k serverKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Stop, k.Clear, k.Back, k.Help}
}
func (k serverKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

type executorKeyMap struct {
	Enter key.Binding
	Add   key.Binding
	Del   key.Binding
	Back  key.Binding
	Help  key.Binding
}

func (k executorKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Add, k.Del, k.Back, k.Help}
}
func (k executorKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

var keys = struct {
	Home        homeKeyMap
	ModelList   modelListKeyMap
	Explore     exploreKeyMap
	FileBrowser fileBrowserKeyMap
	ProfileList profileListKeyMap
	Confirm     confirmKeyMap
	Server      serverKeyMap
	Executor    executorKeyMap
}{
	Home: homeKeyMap{
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		All:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all models")),
		Folders:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "folders")),
		Configure: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "executor")),
		Theme:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	ModelList: modelListKeyMap{
		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Rescan: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rescan")),
		Filter: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Explore: exploreKeyMap{
		Add:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Del:  key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "remove")),
		Sync: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync all")),
		Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	FileBrowser: fileBrowserKeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "nav")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "nav")),
		Back:   key.NewBinding(key.WithKeys("left", "backspace", "b"), key.WithHelp("←", "back")),
		Open:   key.NewBinding(key.WithKeys("right", "/"), key.WithHelp("→", "open")),
		Home:   key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "home")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	},
	ProfileList: profileListKeyMap{
		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "launch")),
		Edit:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Confirm: confirmKeyMap{
		Launch: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "launch")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Server: serverKeyMap{
		Stop:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
		Clear: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear logs")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop & back")),
		Help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
	Executor: executorKeyMap{
		Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "set default")),
		Add:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Del:   key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "use detected")),
		Back:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "save & back")),
		Help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	},
}
