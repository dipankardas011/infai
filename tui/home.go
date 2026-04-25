package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dipankardas011/infai/config"
	"github.com/dipankardas011/infai/db"
)

type HomeModel struct {
	recentModels []db.RecentEntry
	folders      []string
	executor     string
	cursor       int
	width        int
	height       int
}

func NewHomeModel(recent []db.RecentEntry, folders []string, executor string, w, h int) HomeModel {
	return HomeModel{
		recentModels: recent,
		folders:      folders,
		executor:     executor,
		width:        w,
		height:       h,
	}
}

func (m HomeModel) SetSize(w, h int) HomeModel {
	m.width, m.height = w, h
	return m
}

func (m HomeModel) Update(msg tea.Msg) (HomeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.recentModels)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m HomeModel) Selected() (db.RecentEntry, bool) {
	if len(m.recentModels) == 0 {
		return db.RecentEntry{}, false
	}
	return m.recentModels[m.cursor], true
}

func (m HomeModel) View() string {
	t := ActiveTheme
	logoStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(t.Secondary)

	header := lipgloss.JoinHorizontal(lipgloss.Bottom,
		logoStyle.Render(logoASCII),
		versionStyle.Render("  ("+config.Version()+")"),
	)

	hrWidth := 60
	if m.width < 60 {
		hrWidth = m.width - 4
	}
	if hrWidth < 0 {
		hrWidth = 0
	}

	hr := lipgloss.NewStyle().
		Foreground(t.Muted).
		Render(strings.Repeat("─", hrWidth))

	// Recents Section
	recentTitle := styleKey.Render("Recents") + styleMuted.Render(" [a]ll")
	var recentItems []string
	if len(m.recentModels) == 0 {
		recentItems = append(recentItems, styleMuted.Render("  No recents found. Press [a] to see all or [f] to add folders."))
	} else {
		for i, entry := range m.recentModels {
			prefix := "  "
			style := lipgloss.NewStyle().Foreground(t.Text)
			if i == m.cursor {
				prefix = styleSelected.Render("▶ ")
				style = styleSelected
			}
			label := fmt.Sprintf("%s (%s)", entry.Model.DisplayName, entry.Profile.Name)
			recentItems = append(recentItems, prefix+style.Render(label))
		}
	}
	recentBox := lipgloss.JoinVertical(lipgloss.Left,
		recentTitle,
		lipgloss.JoinVertical(lipgloss.Left, recentItems...),
	)

	// Folders Section
	folderTitle := styleKey.Render("Folders") + styleMuted.Render(" [f]olders")
	var folderItems []string
	if len(m.folders) == 0 {
		folderItems = append(folderItems, styleMuted.Render("  No folders configured."))
	} else {
		displayFolders := m.folders
		if len(displayFolders) > 2 {
			displayFolders = displayFolders[:2]
		}
		for _, f := range displayFolders {
			folderItems = append(folderItems, "  "+styleSuccess.Render(f))
		}
		if len(m.folders) > 2 {
			folderItems = append(folderItems, styleMuted.Render(fmt.Sprintf("  ... and %d more", len(m.folders)-2)))
		}
	}
	folderBox := lipgloss.JoinVertical(lipgloss.Left,
		folderTitle,
		lipgloss.JoinVertical(lipgloss.Left, folderItems...),
	)

	// Executor Section
	execTitle := styleKey.Render("Executor") + styleMuted.Render(" [c]onfigure")
	execVal := "  " + styleMuted.Render("Not set (llama-server)")
	if m.executor != "" {
		execVal = "  " + styleSuccess.Render(m.executor)
	}
	execBox := lipgloss.JoinVertical(lipgloss.Left,
		execTitle,
		execVal,
	)

	// Layout containers
	containerWidth := 60
	if m.width < 60 {
		containerWidth = m.width - 4
	}
	if containerWidth < 0 {
		containerWidth = 0
	}

	containerStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(containerWidth)

	content := lipgloss.JoinVertical(lipgloss.Center,
		header,
		hr,
		containerStyle.Render(recentBox),
		containerStyle.Render(folderBox),
		containerStyle.Render(execBox),
		"\n"+styleHelp.Render("enter: select  q: quit  t: theme"),
	)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}
