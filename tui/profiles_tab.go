package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/db"
)

// ── Messages ───────────────────────────────────────────────────────────────
type profilesTabLaunchMsg struct{ entry db.RecentEntry }
type profilesTabNewProfileMsg struct{}
type profilesTabEditProfileMsg struct{ entry db.ProfileEntry }
type profilesTabDeleteProfileMsg struct{ id int64 }

// ── List item & delegate ───────────────────────────────────────────────────
type profileTabItem struct {
	label    string
	entry    *db.ProfileEntry
	recent   *db.RecentEntry
	isNew    bool
	isSep    bool
	isRecent bool
}

func (p profileTabItem) FilterValue() string {
	switch {
	case p.isNew:
		return "new profile"
	case p.isSep:
		return ""
	case p.recent != nil:
		return p.recent.Profile.Name + " " + p.recent.Model.DisplayName
	case p.entry != nil:
		return p.entry.Profile.Name + " " + p.entry.Model.DisplayName
	}
	return ""
}
func (p profileTabItem) Title() string { return p.label }
func (p profileTabItem) Description() string {
	if p.isSep || p.isNew {
		return ""
	}
	if p.recent != nil {
		return "recent · " + p.recent.Model.DisplayName
	}
	if p.entry != nil {
		return p.entry.Model.DisplayName
	}
	return ""
}

type profileTabDelegate struct{}

func (d profileTabDelegate) Height() int                             { return 2 }
func (d profileTabDelegate) Spacing() int                            { return 0 }
func (d profileTabDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d profileTabDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(profileTabItem)
	if !ok {
		return
	}
	sel := index == m.Index()
	prefix := "  "
	nameStyle := lipgloss.NewStyle().Foreground(colorText)
	descStyle := styleMuted

	if i.isSep {
		fmt.Fprintf(w, "  %s\n  ", styleMuted.Render(strings.Repeat("─", m.Width()-4)))
		return
	}
	if i.isNew {
		nameStyle = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	}
	if i.isRecent {
		nameStyle = lipgloss.NewStyle().Foreground(colorSecondary)
	}
	if sel {
		prefix = styleSelected.Render("▶ ")
		nameStyle = styleSelected
	}
	desc := ""
	if i.recent != nil {
		desc = i.recent.Model.DisplayName
	} else if i.entry != nil {
		desc = i.entry.Model.DisplayName
	}
	if desc != "" {
		fmt.Fprintf(w, "%s%s\n  %s", prefix, nameStyle.Render(i.label), descStyle.Render(desc))
	} else {
		fmt.Fprintf(w, "%s%s", prefix, nameStyle.Render(i.label))
	}
}

// ── Model ──────────────────────────────────────────────────────────────────
type ProfilesTabModel struct {
	list     list.Model
	viewport viewport.Model
	recents  []db.RecentEntry

	deleteConfirm bool
	deleteID      int64
	lastPreviewID int64

	width  int
	height int
}

func NewProfilesTabModel(recents []db.RecentEntry, all []db.ProfileEntry, w, h int) ProfilesTabModel {
	items := buildProfileTabItems(recents, all)
	l := list.New(items, profileTabDelegate{}, 0, 0)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowTitle(false)
	l.Styles.NoItems = styleMuted.Copy()

	vp := viewport.New(0, 0)

	m := ProfilesTabModel{
		list:     l,
		viewport: vp,
		recents:  recents,
		width:    w,
		height:   h,
	}
	return m.resize(w, h)
}

func buildProfileTabItems(recents []db.RecentEntry, all []db.ProfileEntry) []list.Item {
	var items []list.Item
	items = append(items, profileTabItem{isNew: true, label: "+ New Profile"})

	recentIDs := make(map[int64]bool)
	for i, r := range recents {
		if i >= 3 {
			break
		}
		recentIDs[r.Profile.ID] = true
		items = append(items, profileTabItem{label: r.Profile.Name, recent: &recents[i], isRecent: true})
	}

	hasMore := false
	for _, p := range all {
		if !recentIDs[p.Profile.ID] {
			hasMore = true
			break
		}
	}
	if len(recents) > 0 && hasMore {
		items = append(items, profileTabItem{isSep: true})
	}

	for i := range all {
		if recentIDs[all[i].Profile.ID] {
			continue
		}
		items = append(items, profileTabItem{label: all[i].Profile.Name, entry: &all[i]})
	}
	return items
}

// resize recalculates widths in one place so SetSize and View stay consistent.
// h is the exact height allocated by HomeModel after header/tabs/footer are
// accounted for. Panels must not exceed it; long content scrolls internally.
func (m ProfilesTabModel) resize(w, h int) ProfilesTabModel {
	m.width = w
	m.height = h

	panelH := h
	if panelH < 3 {
		panelH = 3
	}

	leftW := w / 3
	if leftW < 22 {
		leftW = 22
	}
	// Always leave usable room for the preview panel.
	if leftW > w-26 {
		leftW = w - 26
	}
	if leftW < 16 {
		leftW = 16
	}
	rightW := w - leftW - 1
	if rightW < 20 {
		rightW = 20
	}

	innerLeftW := max(leftW-2, 1)
	innerRightW := max(rightW-2, 1)
	innerH := max(panelH-2, 1)

	m.list.SetSize(innerLeftW, innerH)
	m.viewport.Width = innerRightW
	m.viewport.Height = innerH

	return m
}

func (m ProfilesTabModel) SetSize(w, h int) ProfilesTabModel { return m.resize(w, h) }

func (m ProfilesTabModel) SetData(recents []db.RecentEntry, all []db.ProfileEntry) ProfilesTabModel {
	m.recents = recents
	items := buildProfileTabItems(recents, all)
	m.list.SetItems(items)
	m.deleteConfirm = false
	m.lastPreviewID = 0
	return m
}

func (m ProfilesTabModel) selectedEntry() *db.ProfileEntry {
	item, ok := m.list.SelectedItem().(profileTabItem)
	if !ok || item.isNew || item.isSep {
		return nil
	}
	if item.recent != nil {
		return &db.ProfileEntry{Model: item.recent.Model, Profile: item.recent.Profile}
	}
	return item.entry
}

func (m *ProfilesTabModel) updateViewport() {
	t := ActiveTheme
	entry := m.selectedEntry()

	if entry == nil {
		m.viewport.SetContent(styleMuted.Italic(true).Render("select a profile\nto view configuration"))
		return
	}

	if entry.Profile.ID != m.lastPreviewID {
		m.viewport.GotoTop()
		m.lastPreviewID = entry.Profile.ID
	}

	p := entry.Profile
	m2 := entry.Model

	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	sectionStyle := lipgloss.NewStyle().Foreground(t.Secondary).Bold(true)
	fieldLabel := lipgloss.NewStyle().Foreground(t.Muted).Width(14).Align(lipgloss.Right)
	fieldVal := lipgloss.NewStyle().Foreground(t.Text)
	check := lipgloss.NewStyle().Foreground(t.Success).Render("✓")

	var sb strings.Builder

	sb.WriteString(titleStyle.Render(p.Name) + "\n")
	sb.WriteString(fieldLabel.Render("Model") + "  " + fieldVal.Render(m2.DisplayName) + "\n\n")

	sb.WriteString(sectionStyle.Render("Network") + "\n")
	sb.WriteString(fieldLabel.Render("Host") + "  " + fieldVal.Render(p.Host) + "\n")
	sb.WriteString(fieldLabel.Render("Port") + "  " + fieldVal.Render(fmt.Sprintf("%d", p.Port)) + "\n\n")

	sb.WriteString(sectionStyle.Render("Model Config") + "\n")
	sb.WriteString(fieldLabel.Render("Context") + "  " + fieldVal.Render(fmtInt(p.ContextSize)) + "\n")
	sb.WriteString(fieldLabel.Render("GPU Layers") + "  " + fieldVal.Render(p.NGL) + "\n")
	if p.BatchSize != nil {
		sb.WriteString(fieldLabel.Render("Batch Size") + "  " + fieldVal.Render(fmt.Sprintf("%d", *p.BatchSize)) + "\n")
	}
	if p.UBatchSize != nil {
		sb.WriteString(fieldLabel.Render("UBatch") + "  " + fieldVal.Render(fmt.Sprintf("%d", *p.UBatchSize)) + "\n")
	}
	if p.CacheTypeK != nil {
		sb.WriteString(fieldLabel.Render("Cache K") + "  " + fieldVal.Render(*p.CacheTypeK) + "\n")
	}
	if p.CacheTypeV != nil {
		sb.WriteString(fieldLabel.Render("Cache V") + "  " + fieldVal.Render(*p.CacheTypeV) + "\n")
	}
	sb.WriteString("\n")

	if p.FlashAttn || p.Jinja || p.NoKVOffload || p.UseMmproj {
		sb.WriteString(sectionStyle.Render("Flags") + "\n")
		if p.FlashAttn {
			sb.WriteString(fieldLabel.Render("Flash Attn") + "  " + check + "\n")
		}
		if p.Jinja {
			sb.WriteString(fieldLabel.Render("Jinja") + "  " + check + "\n")
		}
		if p.NoKVOffload {
			sb.WriteString(fieldLabel.Render("No KV Offload") + "  " + check + "\n")
		}
		if p.UseMmproj {
			sb.WriteString(fieldLabel.Render("Mmproj") + "  " + check + "\n")
		}
		sb.WriteString("\n")
	}

	if p.Temperature != nil || p.TopP != nil || p.TopK != nil || p.ReasoningBudget != nil {
		sb.WriteString(sectionStyle.Render("Sampling") + "\n")
		if p.Temperature != nil {
			sb.WriteString(fieldLabel.Render("Temp") + "  " + fieldVal.Render(fmt.Sprintf("%.2f", *p.Temperature)) + "\n")
		}
		if p.TopP != nil {
			sb.WriteString(fieldLabel.Render("Top P") + "  " + fieldVal.Render(fmt.Sprintf("%.2f", *p.TopP)) + "\n")
		}
		if p.TopK != nil {
			sb.WriteString(fieldLabel.Render("Top K") + "  " + fieldVal.Render(fmt.Sprintf("%d", *p.TopK)) + "\n")
		}
		if p.ReasoningBudget != nil {
			sb.WriteString(fieldLabel.Render("Reasoning") + "  " + fieldVal.Render(fmt.Sprintf("%d", *p.ReasoningBudget)) + "\n")
		}
		sb.WriteString("\n")
	}

	if p.ExtraFlags != "" {
		sb.WriteString(sectionStyle.Render("Extra") + "\n")
		sb.WriteString("  " + styleMuted.Render(p.ExtraFlags) + "\n")
	}

	m.viewport.SetContent(sb.String())
}

func (m ProfilesTabModel) Update(msg tea.Msg) (ProfilesTabModel, tea.Cmd) {
	if m.deleteConfirm {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				id := m.deleteID
				m.deleteConfirm = false
				m.deleteID = 0
				return m, func() tea.Msg { return profilesTabDeleteProfileMsg{id: id} }
			case "n", "esc":
				m.deleteConfirm = false
				m.deleteID = 0
				m.updateViewport()
				return m, nil
			}
		}
		return m, nil
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter":
			item, ok := m.list.SelectedItem().(profileTabItem)
			if !ok || item.isSep {
				return m, nil
			}
			if item.isNew {
				return m, func() tea.Msg { return profilesTabNewProfileMsg{} }
			}
			entry := m.selectedEntry()
			if entry != nil {
				return m, func() tea.Msg {
					return profilesTabLaunchMsg{entry: db.RecentEntry{Model: entry.Model, Profile: entry.Profile}}
				}
			}
			return m, nil
		case "e":
			entry := m.selectedEntry()
			if entry != nil {
				return m, func() tea.Msg { return profilesTabEditProfileMsg{entry: *entry} }
			}
			return m, nil
		case "x":
			entry := m.selectedEntry()
			if entry != nil {
				m.deleteConfirm = true
				m.deleteID = entry.Profile.ID
				return m, nil
			}
			return m, nil
		}
	}

	// Preview panel scrolling. Keep list navigation on ↑/↓ and use page keys
	// for the right-side profile details when they overflow.
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.updateViewport()
	return m, cmd
}

func (m ProfilesTabModel) View() string {
	if m.deleteConfirm {
		return m.deleteConfirmView()
	}

	t := ActiveTheme
	// Work on a resized copy so View also stays correct after terminal changes.
	m = m.resize(m.width, m.height)
	m.updateViewport()

	leftW := m.list.Width() + 2
	rightW := m.viewport.Width + 2
	panelH := m.height
	if panelH < 3 {
		panelH = 3
	}

	// Left panel
	leftBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Width(leftW - 2).
		Height(panelH - 2).
		MaxHeight(panelH).
		Render(m.list.View())

	// Right panel
	rightBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Width(rightW - 2).
		Height(panelH - 2).
		MaxHeight(panelH).
		Render(m.viewport.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func (m ProfilesTabModel) deleteConfirmView() string {
	t := ActiveTheme
	msg := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Foreground(t.Error).Bold(true).Render("Delete this profile?"),
		"",
		styleKey.Render("y")+styleMuted.Render(": confirm")+"  "+
			styleKey.Render("n")+styleMuted.Render(": cancel"),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Error).
			Padding(2, 4).
			MaxHeight(max(m.height, 1)).
			Render(msg))
}
