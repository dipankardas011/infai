package tui

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name      string
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color // caution states: stopping, port fallback, mid-load bars
	Error     lipgloss.Color
	Muted     lipgloss.Color
	Text      lipgloss.Color
	Bg        lipgloss.Color
	Surface   lipgloss.Color // subtle raised background for selected rows
}

var ThemeList = []Theme{
	{
		Name:      "tokyonight",
		Primary:   "#7DCFFF",
		Secondary: "#BB9AF7",
		Success:   "#9ECE6A",
		Warning:   "#E0AF68",
		Error:     "#F7768E",
		Muted:     "#565F89",
		Text:      "#C0CAF5",
		Bg:        "#1A1B26",
		Surface:   "#292E42",
	},
	{
		Name:      "everforest",
		Primary:   "#83C092",
		Secondary: "#D699B6",
		Success:   "#A7C080",
		Warning:   "#DBBC7F",
		Error:     "#E67E80",
		Muted:     "#7A8478",
		Text:      "#D3C6AA",
		Bg:        "#2D353B",
		Surface:   "#374145",
	},
	{
		Name:      "onedark",
		Primary:   "#61AFEF",
		Secondary: "#C678DD",
		Success:   "#98C379",
		Warning:   "#E5C07B",
		Error:     "#E06C75",
		Muted:     "#5C6370",
		Text:      "#ABB2BF",
		Bg:        "#282C34",
		Surface:   "#3E4451",
	},
	{
		Name:      "rosepine",
		Primary:   "#9CCFD8",
		Secondary: "#C4A7E7",
		Success:   "#31748F",
		Warning:   "#F6C177",
		Error:     "#EB6F92",
		Muted:     "#6E6A86",
		Text:      "#E0DEF4",
		Bg:        "#191724",
		Surface:   "#26233A",
	},
	{
		Name:      "gruvbox",
		Primary:   "#83A598",
		Secondary: "#D3869B",
		Success:   "#B8BB26",
		Warning:   "#FABD2F",
		Error:     "#FB4934",
		Muted:     "#928374",
		Text:      "#EBDBB2",
		Bg:        "#282828",
		Surface:   "#3C3836",
	},
	{
		Name:      "catppuccin",
		Primary:   "#89B4FA",
		Secondary: "#CBA6F7",
		Success:   "#A6E3A1",
		Warning:   "#F9E2AF",
		Error:     "#F38BA8",
		Muted:     "#6C7086",
		Text:      "#CDD6F4",
		Bg:        "#1E1E2E",
		Surface:   "#313244",
	},
	{
		Name:      "nord",
		Primary:   "#88C0D0",
		Secondary: "#B48EAD",
		Success:   "#A3BE8C",
		Warning:   "#EBCB8B",
		Error:     "#BF616A",
		Muted:     "#4C566A",
		Text:      "#D8DEE9",
		Bg:        "#2E3440",
		Surface:   "#3B4252",
	},
	{
		Name:      "dracula",
		Primary:   "#8BE9FD",
		Secondary: "#BD93F9",
		Success:   "#50FA7B",
		Warning:   "#F1FA8C",
		Error:     "#FF5555",
		Muted:     "#6272A4",
		Text:      "#F8F8F2",
		Bg:        "#282A36",
		Surface:   "#44475A",
	},
	{
		Name:      "kanagawa",
		Primary:   "#7E9CD8",
		Secondary: "#957FB8",
		Success:   "#98BB6C",
		Warning:   "#C0A36E",
		Error:     "#C34043",
		Muted:     "#727169",
		Text:      "#DCD7BA",
		Bg:        "#1F1F28",
		Surface:   "#2A2A37",
	},
	{
		Name:      "solarized",
		Primary:   "#268BD2",
		Secondary: "#6C71C4",
		Success:   "#859900",
		Warning:   "#B58900",
		Error:     "#DC322F",
		Muted:     "#586E75",
		Text:      "#93A1A1",
		Bg:        "#002B36",
		Surface:   "#073642",
	},
	{
		Name:      "monokai",
		Primary:   "#66D9EF",
		Secondary: "#AE81FF",
		Success:   "#A6E22E",
		Warning:   "#E6DB74",
		Error:     "#F92672",
		Muted:     "#75715E",
		Text:      "#F8F8F2",
		Bg:        "#272822",
		Surface:   "#3E3D32",
	},
}

var themeMap = func() map[string]Theme {
	m := make(map[string]Theme)
	for _, t := range ThemeList {
		m[t.Name] = t
	}
	return m
}()

var (
	ActiveTheme = ThemeList[0]
	themeIdx    = 0
)

func SetTheme(name string) {
	if t, ok := themeMap[name]; ok {
		ActiveTheme = t
		for i, th := range ThemeList {
			if th.Name == name {
				themeIdx = i
				break
			}
		}
		rebuildStyles()
	}
}

// CycleTheme advances to the next theme and returns its name.
func CycleTheme() string {
	themeIdx = (themeIdx + 1) % len(ThemeList)
	ActiveTheme = ThemeList[themeIdx]
	rebuildStyles()
	return ActiveTheme.Name
}
