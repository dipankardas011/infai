package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// hitlPolicyTools is the built-in tool set the form asks about. MCP tools are
// not known until a session connects, so an unlisted tool keeps the engine's
// human fallback.
var hitlPolicyTools = []contracts.ToolType{
	contracts.ReadTool,
	contracts.ListTool,
	contracts.GlobTool,
	contracts.SearchTool,
	contracts.ReadSkillTool,
	contracts.TaskChecklistTool,
	contracts.WriteTool,
	contracts.EditTool,
	contracts.BashTool,
	contracts.WebFetchTool,
	contracts.WebSearchTool,
	contracts.DelegateSidecarLoopTool,
	contracts.DelegateBackgroundSidecarLoopTool,
}

// hitlDecisionCycle is the order the arrows walk: left moves towards deny, right
// towards allow, and the value wraps at either end.
var hitlDecisionCycle = []auditor.PolicyDecision{
	auditor.DenyPolicy,
	auditor.HumanPolicy,
	auditor.AllowPolicy,
}

type hitlPolicyRow struct {
	tool     contracts.ToolType
	decision auditor.PolicyDecision
}

type hitlPolicyForm struct {
	name          textinput.Model
	rows          []hitlPolicyRow
	taken         map[string]bool
	focus         int
	toolWidth     int
	width, height int
	err           string
	canceled      bool
	policy        glue.HITLPolicyView
}

func runHITLPolicyForm(ctx context.Context, in io.Reader, out io.Writer, name string, taken map[string]bool) (glue.HITLPolicyView, error) {
	nameInput := textinput.New()
	nameInput.Placeholder = "letters, digits, _ or -"
	nameInput.SetValue(name)
	nameInput.SetWidth(40)
	// The field is a cell of the panel: without its own surface background the
	// terminal's background shows through it.
	textStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text)
	placeholderStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Muted)
	inputStyles := nameInput.Styles()
	inputStyles.Focused.Prompt = placeholderStyle
	inputStyles.Focused.Text = textStyle
	inputStyles.Focused.Placeholder = placeholderStyle
	inputStyles.Focused.Suggestion = placeholderStyle
	inputStyles.Blurred = inputStyles.Focused
	inputStyles.Cursor.Color = everforest.Green
	nameInput.SetStyles(inputStyles)

	rows := make([]hitlPolicyRow, len(hitlPolicyTools))
	toolWidth := len("Tool")
	for i, tool := range hitlPolicyTools {
		rows[i] = hitlPolicyRow{tool: tool, decision: auditor.HumanPolicy}
		toolWidth = max(toolWidth, len(tool))
	}
	model := &hitlPolicyForm{
		name:      nameInput,
		rows:      rows,
		taken:     taken,
		toolWidth: toolWidth,
	}
	result, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return glue.HITLPolicyView{}, err
	}
	form := result.(*hitlPolicyForm)
	if form.canceled {
		return glue.HITLPolicyView{}, errSelectionCanceled
	}
	return form.policy, nil
}

func (m *hitlPolicyForm) Init() tea.Cmd {
	return m.name.Focus()
}

func (m *hitlPolicyForm) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.name.SetWidth(max(10, min(48, msg.Width-10)))
		return m, nil
	case tea.PasteMsg:
		if m.focus == 0 {
			var cmd tea.Cmd
			m.name, cmd = m.name.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *hitlPolicyForm) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	case "enter", "ctrl+s":
		if err := m.save(); err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m, tea.Quit
	case "tab", "down":
		return m, m.moveFocus(1)
	case "shift+tab", "up":
		return m, m.moveFocus(-1)
	case "left", "h":
		if m.focus > 0 {
			m.cycleDecision(-1)
			return m, nil
		}
	case "right", "l":
		if m.focus > 0 {
			m.cycleDecision(1)
			return m, nil
		}
	}
	if m.focus == 0 {
		var cmd tea.Cmd
		m.name, cmd = m.name.Update(msg)
		m.err = ""
		return m, cmd
	}
	return m, nil
}

func (m *hitlPolicyForm) moveFocus(delta int) tea.Cmd {
	count := len(m.rows) + 1
	m.focus = (m.focus + delta + count) % count
	if m.focus == 0 {
		return m.name.Focus()
	}
	m.name.Blur()
	return nil
}

func (m *hitlPolicyForm) cycleDecision(delta int) {
	row := &m.rows[m.focus-1]
	current := 0
	for i, decision := range hitlDecisionCycle {
		if decision == row.decision {
			current = i
			break
		}
	}
	row.decision = hitlDecisionCycle[(current+delta+len(hitlDecisionCycle))%len(hitlDecisionCycle)]
	m.err = ""
}

func (m *hitlPolicyForm) save() error {
	name := strings.TrimSpace(m.name.Value())
	if name == auditor.DefaultHITLPolicyName {
		return hitlPolicyReadOnly(name)
	}
	if m.taken[name] {
		return fmt.Errorf("HITL policy %q already exists", name)
	}
	decisions := make(contracts.HITLPolicyConfig, len(m.rows))
	for _, row := range m.rows {
		decisions[string(row.tool)] = row.decision.String()
	}
	if err := store.ValidatePolicy(name, decisions); err != nil {
		return err
	}
	m.policy = glue.HITLPolicyView{Name: name, Decisions: decisions}
	return nil
}

// hitlFormStyles paints every form style with the panel surface, so a
// foreground-only style cannot drop a cell to the terminal's background.
func hitlFormStyles() harnessStyles {
	styles := newHarnessStyles()
	surface := everforest.Surface
	styles.screenBody = styles.screenBody.Background(surface)
	styles.muted = styles.muted.Background(surface)
	styles.active = styles.active.Background(surface)
	styles.inactive = styles.inactive.Background(surface)
	styles.error = styles.error.Background(surface)
	styles.screenRow = styles.screenRow.Background(surface)
	styles.menu = styles.menu.BorderBackground(surface)
	return styles
}

func (m *hitlPolicyForm) View() tea.View {
	styles := hitlFormStyles()
	nameLabel := styles.screenBody
	if m.focus == 0 {
		nameLabel = styles.active
	}
	lines := []string{
		styles.heading("Add HITL policy", everforest.Surface),
		"",
		nameLabel.Render("Name"),
		m.name.View(),
		"",
		styles.screenBody.Render("Tool decisions — ←/→ change"),
	}
	for i, row := range m.rows {
		lines = append(lines, m.rowView(styles, row, m.focus == i+1))
	}
	lines = append(lines, styles.muted.Render("Unlisted tools require human approval."))
	if m.err != "" {
		lines = append(lines, "", styles.error.Render(m.err))
	}
	lines = append(lines, "", styles.muted.Render("tab/↑↓ move • ←/→ change • enter save • esc cancel"))
	content := styles.menu.Padding(1, 2).Render(strings.Join(lines, "\n"))
	if m.width > 0 && m.height > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = appName + " · HITL policy"
	view.BackgroundColor = themeBackground()
	view.ForegroundColor = themeForeground()
	return view
}

func (m *hitlPolicyForm) rowView(styles harnessStyles, row hitlPolicyRow, focused bool) string {
	marker, label := "  ", styles.inactive
	if focused {
		marker, label = "▸ ", styles.active
	}
	gap := lipgloss.NewStyle().Background(everforest.Surface)
	line := label.Render(marker + fmt.Sprintf("%-*s", m.toolWidth, row.tool))
	value := " " + row.decision.String() + " "
	if focused {
		selected := lipgloss.NewStyle().Background(everforest.SelectionBg).Foreground(everforest.Yellow).Bold(true)
		return line + gap.Render("  ") + styles.muted.Render("◀") + selected.Render(value) + styles.muted.Render("▶")
	}
	return line + gap.Render("  ") + styles.muted.Render("◀") + hitlDecisionStyle(row.decision).Background(everforest.Surface).Render(value) + styles.muted.Render("▶")
}
