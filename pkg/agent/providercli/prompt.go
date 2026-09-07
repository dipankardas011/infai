package providercli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	agentstyle "github.com/dipankardas011/infai/pkg/agent/style"
)

var errCanceled = errors.New("canceled")

type menuModel struct {
	title    string
	items    []string
	cursor   int
	selected string
	canceled bool
}

func newMenuModel(title string, items []string) *menuModel {
	return &menuModel{title: title, items: append([]string(nil), items...)}
}

func (m *menuModel) Init() tea.Cmd { return nil }

func (m *menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.items) > 0 {
			m.selected = m.items[m.cursor]
		}
		return m, tea.Quit
	case "esc", "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *menuModel) View() tea.View {
	p := agentstyle.Everforest
	title := lipgloss.NewStyle().Foreground(p.Green).Bold(true).Render(strings.ToUpper(m.title))
	muted := lipgloss.NewStyle().Foreground(p.Muted)
	active := lipgloss.NewStyle().Background(p.SurfaceAlt).Foreground(p.Yellow).Bold(true)
	rows := make([]string, 0, len(m.items))
	for i, item := range m.items {
		row := "  " + item
		if i == m.cursor {
			row = active.Render("> " + item)
		}
		rows = append(rows, row)
	}
	body := title + "\n\n" + strings.Join(rows, "\n") + "\n\n" + muted.Render("up/down or j/k  enter select  esc cancel")
	card := lipgloss.NewStyle().Background(p.Surface).Foreground(p.Text).Border(lipgloss.NormalBorder()).BorderForeground(p.Green).Padding(1, 2).Render(body)
	return tea.NewView(card)
}

type inputModel struct {
	title    string
	input    textinput.Model
	value    string
	canceled bool
}

func newInputModel(title, initial string, password bool) *inputModel {
	in := textinput.New()
	in.Prompt = "> "
	in.SetValue(initial)
	in.CursorEnd()
	in.SetWidth(48)
	in.SetVirtualCursor(true)
	if password {
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '*'
	}
	p := agentstyle.Everforest
	s := in.Styles()
	s.Focused.Text = lipgloss.NewStyle().Foreground(p.Text)
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(p.Green).Bold(true)
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(p.Muted)
	s.Blurred = s.Focused
	s.Cursor.Color = p.Green
	in.SetStyles(s)
	return &inputModel{title: title, input: in}
}

func (m *inputModel) Init() tea.Cmd { return m.input.Focus() }

func (m *inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			m.value = strings.TrimSpace(m.input.Value())
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *inputModel) View() tea.View {
	p := agentstyle.Everforest
	title := lipgloss.NewStyle().Foreground(p.Green).Bold(true).Render(strings.ToUpper(m.title))
	help := lipgloss.NewStyle().Foreground(p.Muted).Render("enter accept  esc cancel")
	body := title + "\n\n" + m.input.View() + "\n\n" + help
	card := lipgloss.NewStyle().Background(p.Surface).Foreground(p.Text).Border(lipgloss.NormalBorder()).BorderForeground(p.Green).Padding(1, 2).Render(body)
	v := tea.NewView(card)
	v.Cursor = m.input.Cursor()
	return v
}

func runMenu(in io.Reader, out io.Writer, title string, items []string) (string, error) {
	if len(items) == 0 {
		return "", errors.New("no choices available")
	}
	m := newMenuModel(title, items)
	result, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return "", err
	}
	selected := result.(*menuModel)
	if selected.canceled {
		return "", errCanceled
	}
	return selected.selected, nil
}

func runInput(in io.Reader, out io.Writer, title, initial string, password bool) (string, error) {
	m := newInputModel(title, initial, password)
	result, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return "", err
	}
	completed := result.(*inputModel)
	if completed.canceled {
		return "", errCanceled
	}
	return completed.value, nil
}

func requiredInput(in io.Reader, out io.Writer, title, initial string, password bool) (string, error) {
	value, err := runInput(in, out, title, initial, password)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(title))
	}
	return value, nil
}
