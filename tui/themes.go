package tui

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name      string
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Error     lipgloss.Color
	Muted     lipgloss.Color
	Text      lipgloss.Color
	Bg        lipgloss.Color
}

var ThemeList = []Theme{
	{
		Name:      "tokyonight",
		Primary:   "#7DCFFF",
		Secondary: "#BB9AF7",
		Success:   "#9ECE6A",
		Error:     "#F7768E",
		Muted:     "#565F89",
		Text:      "#C0CAF5",
		Bg:        "#1A1B26",
	},
	{
		Name:      "everforest",
		Primary:   "#83C092",
		Secondary: "#D699B6",
		Success:   "#A7C080",
		Error:     "#E67E80",
		Muted:     "#7A8478",
		Text:      "#D3C6AA",
		Bg:        "#2D353B",
	},
	{
		Name:      "onedark",
		Primary:   "#61AFEF",
		Secondary: "#C678DD",
		Success:   "#98C379",
		Error:     "#E06C75",
		Muted:     "#5C6370",
		Text:      "#ABB2BF",
		Bg:        "#282C34",
	},
	{
		Name:      "rosepine",
		Primary:   "#9CCFD8",
		Secondary: "#C4A7E7",
		Success:   "#31748F",
		Error:     "#EB6F92",
		Muted:     "#6E6A86",
		Text:      "#E0DEF4",
		Bg:        "#191724",
	},
	{
		Name:      "gruvbox",
		Primary:   "#83A598",
		Secondary: "#D3869B",
		Success:   "#B8BB26",
		Error:     "#FB4934",
		Muted:     "#928374",
		Text:      "#EBDBB2",
		Bg:        "#282828",
	},
	{
		Name:      "catppuccin",
		Primary:   "#89B4FA",
		Secondary: "#CBA6F7",
		Success:   "#A6E3A1",
		Error:     "#F38BA8",
		Muted:     "#6C7086",
		Text:      "#CDD6F4",
		Bg:        "#1E1E2E",
	},
	{
		Name:      "nord",
		Primary:   "#88C0D0",
		Secondary: "#B48EAD",
		Success:   "#A3BE8C",
		Error:     "#BF616A",
		Muted:     "#4C566A",
		Text:      "#D8DEE9",
		Bg:        "#2E3440",
	},
	{
		Name:      "dracula",
		Primary:   "#8BE9FD",
		Secondary: "#BD93F9",
		Success:   "#50FA7B",
		Error:     "#FF5555",
		Muted:     "#6272A4",
		Text:      "#F8F8F2",
		Bg:        "#282A36",
	},
	{
		Name:      "kanagawa",
		Primary:   "#7E9CD8",
		Secondary: "#957FB8",
		Success:   "#98BB6C",
		Error:     "#C34043",
		Muted:     "#727169",
		Text:      "#DCD7BA",
		Bg:        "#1F1F28",
	},
	{
		Name:      "solarized",
		Primary:   "#268BD2",
		Secondary: "#6C71C4",
		Success:   "#859900",
		Error:     "#DC322F",
		Muted:     "#586E75",
		Text:      "#93A1A1",
		Bg:        "#002B36",
	},
	{
		Name:      "monokai",
		Primary:   "#66D9EF",
		Secondary: "#AE81FF",
		Success:   "#A6E22E",
		Error:     "#F92672",
		Muted:     "#75715E",
		Text:      "#F8F8F2",
		Bg:        "#272822",
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
