package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// mcpPairSeparator is how one entry is written in the editor: a name, the
// separator, then the value. It is the editor's own syntax — what is stored is a
// map, not this text — so it lives here, where it is typed and read back.
const mcpPairSeparator = "="

// mcpPair is one name/value entry: a variable of a stdio server's environment,
// or a header of a streamable-http one.
type mcpPair struct {
	name  string
	value string
}

// empty reports whether a row holds nothing: the editor's own shape, not an
// entry of the server.
func (p mcpPair) empty() bool { return p.name == "" && p.value == "" }

// mcpPairsModel edits one pair row: its entries, one per line, in a screen of
// their own. The row itself only says how many there are — a comma separated
// string asks the operator to hold the whole list in their head while typing it.
type mcpPairsModel struct {
	field   *mcpFormField
	focus   int
	editing bool
	input   textinput.Model
	err     string
	width   int
	height  int
	// minWidth is the width the form it was opened from draws its panel at.
	minWidth int
	closed   bool
	// cancelled means the whole form was left, not just this screen.
	cancelled bool
}

func newMCPPairsModel(field *mcpFormField) *mcpPairsModel {
	model := &mcpPairsModel{field: field}
	model.setFocus(0)
	return model
}

// Init implements tea.Model.
func (m *mcpPairsModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *mcpPairsModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.PasteMsg:
		if m.editing {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *mcpPairsModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.editing {
		return m.editKey(msg, key)
	}
	switch key {
	case "ctrl+c":
		m.cancelled = true
		return m, tea.Quit
	case "esc":
		m.sanitize()
		m.closed = true
		return m, nil
	case "up", "k":
		m.moveFocus(-1)
		return m, nil
	case "down", "j", "tab":
		m.moveFocus(1)
		return m, nil
	case "shift+tab":
		m.moveFocus(-1)
		return m, nil
	case "enter", "right", "l":
		// The last row is not an entry: it is where a new one starts.
		if m.focus >= len(m.field.pairs) {
			m.field.pairs = append(m.field.pairs, mcpPair{})
		}
		m.startEdit()
		return m, nil
	case "a":
		m.field.pairs = append(m.field.pairs, mcpPair{})
		m.setFocus(len(m.field.pairs) - 1)
		m.startEdit()
		return m, nil
	case "d", "delete", "backspace":
		if m.focus < len(m.field.pairs) {
			m.field.pairs = append(m.field.pairs[:m.focus], m.field.pairs[m.focus+1:]...)
			m.setFocus(min(m.focus, len(m.field.pairs)))
		}
		return m, nil
	}
	return m, nil
}

// editKey owns the keyboard while one entry is being typed. A row is edited as
// the single line it is written as — name, separator, value — because that is
// the shape an environment variable is set in.
func (m *mcpPairsModel) editKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "ctrl+c":
		// Typing owns the keyboard, but not this: it is how the whole command is
		// abandoned, and a text input has no binding for it.
		m.cancelled = true
		return m, tea.Quit
	case "esc":
		// What was being typed is dropped, and the row it was typed into with
		// it: an entry with no name is not an entry.
		if m.focus < len(m.field.pairs) && m.field.pairs[m.focus].empty() {
			m.field.pairs = append(m.field.pairs[:m.focus], m.field.pairs[m.focus+1:]...)
			m.moveFocus(0)
		}
		m.stopEdit()
		return m, nil
	case "enter":
		pair, err := splitMCPPair(m.input.Value(), m.hint())
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.field.pairs[m.focus] = pair
		m.stopEdit()
		// The next entry opens where the last one ended, so a list is typed the
		// way it is read: one line after another.
		m.field.pairs = append(m.field.pairs, mcpPair{})
		m.setFocus(len(m.field.pairs) - 1)
		m.startEdit()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.err = ""
	return m, cmd
}

// sanitize drops the rows that were never named. They are the shape of the
// editor, not entries of the server.
func (m *mcpPairsModel) sanitize() {
	kept := m.field.pairs[:0]
	for _, pair := range m.field.pairs {
		if !pair.empty() {
			kept = append(kept, pair)
		}
	}
	m.field.pairs = kept
	m.setFocus(min(m.focus, len(m.field.pairs)))
}

func (m *mcpPairsModel) startEdit() {
	m.editing = true
	m.err = ""
	m.input = newMCPFormInput(m.field.pairs[m.focus].line(), m.hint(), false)
	m.input.Focus()
}

func (m *mcpPairsModel) stopEdit() {
	m.editing = false
	m.err = ""
	m.input.Blur()
}

func (m *mcpPairsModel) setFocus(index int) {
	m.focus = index
}

func (m *mcpPairsModel) moveFocus(delta int) {
	total := len(m.field.pairs) + 1 // the last row adds an entry
	m.focus = ((m.focus+delta)%total + total) % total
}

func (m *mcpPairsModel) hint() string { return m.field.hint }

// View implements tea.Model.
func (m *mcpPairsModel) View() tea.View {
	styles := mcpFormStyles()
	body := fmt.Sprintf("%s of this server, one %s per row", strings.ToLower(m.field.label), m.hint())
	lines := []string{styles.heading("MCP server · "+m.field.label, everforest.Surface), styles.screenBody.Render(body), ""}
	for i, pair := range m.field.pairs {
		lines = append(lines, m.row(i, pair.line()))
	}
	if !m.editing {
		lines = append(lines, m.row(len(m.field.pairs), "+ add"))
	}
	if m.err != "" {
		lines = append(lines, "", styles.error.Render(m.err))
	}
	help := "↑/↓ move • enter edit • a add • d delete • esc back"
	if m.editing {
		help = "enter save entry • esc cancel entry"
	}
	lines = append(lines, "", styles.muted.Render(help))
	return mcpScreen(lines, m.width, m.height, "MCP server · "+m.field.label, m.minWidth)
}

func (m *mcpPairsModel) row(index int, text string) string {
	styles := mcpFormStyles()
	marker, label := "  ", styles.inactive
	if index == m.focus {
		marker, label = "▸ ", styles.active
	}
	if index == m.focus && m.editing {
		return label.Render(marker) + m.input.View()
	}
	return label.Render(marker) + styles.screenRow.Render(text)
}

// line is the entry as it is written and typed: its name, then its value. One
// separator is used for both an environment variable and a header, so there is
// only one shape to hold in mind while typing.
func (p mcpPair) line() string {
	if p.empty() {
		return ""
	}
	return p.name + mcpPairSeparator + p.value
}

// splitMCPPair reads back one line. The separator is required: a name on its own
// is a typo, not a name with an empty value.
func splitMCPPair(line, hint string) (mcpPair, error) {
	name, value, found := strings.Cut(line, mcpPairSeparator)
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	switch {
	case !found:
		return mcpPair{}, fmt.Errorf("an entry is written as %s", hint)
	case name == "":
		return mcpPair{}, fmt.Errorf("an entry needs a name, as in %s", hint)
	}
	return mcpPair{name: name, value: value}, nil
}

// mcpPairsNamed is a pair row's entries that were given a name: the rows of the
// editor are also how a list is started, so a nameless row is the shape of the
// screen, not an entry of the server.
func mcpPairsNamed(field *mcpFormField) []mcpPair {
	if field == nil {
		return nil
	}
	named := make([]mcpPair, 0, len(field.pairs))
	for _, pair := range field.pairs {
		if !pair.empty() {
			named = append(named, pair)
		}
	}
	return named
}

// mcpPairsMap is a pair row's entries as the configuration stores them.
func mcpPairsMap(field *mcpFormField) map[string]string {
	named := mcpPairsNamed(field)
	if len(named) == 0 {
		return nil
	}
	entries := make(map[string]string, len(named))
	for _, pair := range named {
		entries[pair.name] = pair.value
	}
	return entries
}

// mcpPairsSummary is a pair row's value in the form: how many entries it holds,
// and which variables they are, so the row is readable without opening it.
func mcpPairsSummary(field *mcpFormField) string {
	named := mcpPairsNamed(field)
	if len(named) == 0 {
		return ""
	}
	names := make([]string, 0, len(named))
	for _, pair := range named {
		names = append(names, pair.name)
	}
	if len(names) == 1 {
		return "1 entry — " + names[0]
	}
	return fmt.Sprintf("%d entries — %s", len(names), strings.Join(names, ", "))
}

// mcpPairsField is a row of entries, written one name=value per line.
func mcpPairsField(key, label, hint string, pairs []mcpPair) mcpFormField {
	return mcpFormField{key: key, label: label, hint: hint, kind: mcpFormPairs, pairs: pairs}
}
