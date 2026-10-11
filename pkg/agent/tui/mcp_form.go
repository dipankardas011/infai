package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// ---- the server form ----

// The form's rows, keyed so visibility, focus and the config the form builds can
// all name the row they mean.
const (
	mcpFieldTransport  = "transport"
	mcpFieldName       = "name"
	mcpFieldURL        = "url"
	mcpFieldTimeout    = "timeout"
	mcpFieldOAuth      = "oauth"
	mcpFieldCredential = "credential"
	mcpFieldBearer     = "bearerToken"
	mcpFieldHeaders    = "headers"
	mcpFieldCommand    = "command"
	mcpFieldArgs       = "args"
	mcpFieldEnv        = "env"
)

// mcpFormInputWidth is how much of a row's value one screen shows. A longer
// value scrolls under the caret, which is what a single-line field can offer.
const mcpFormInputWidth = 46

// mcpFormFieldKind is how one row is edited.
type mcpFormFieldKind int

const (
	// mcpFormText is a row the operator types into, masked when the value is a
	// credential.
	mcpFormText mcpFormFieldKind = iota
	// mcpFormChoice is a row with fixed alternatives, changed with the arrows.
	mcpFormChoice
	// mcpFormPairs is a row of name/value entries, edited in its own screen.
	mcpFormPairs
)

// mcpFormField is one row of the server form.
type mcpFormField struct {
	key   string
	label string
	// hint stands in for an empty row: a value that is left out has to say what
	// belongs there.
	hint string
	kind mcpFormFieldKind
	// choices is a choice row's alternatives, in the order they cycle, and
	// choice is the one it holds.
	choices []string
	choice  int
	// input is a text row's value and caret.
	input textinput.Model
	// pairs is a pair row's entries: a name and a value, written as one
	// "name=value" line in the row's own screen.
	pairs []mcpPair
	// readonly rows are shown but not edited: an edit keeps the name, the
	// transport and the authentication kind of the server it started from.
	readonly bool
}

// mcpFormModel is the MCP server form: every row of one server on one screen, so
// the transport, the authentication kind and the fields they imply are answered
// without leaving the TUI.
type mcpFormModel struct {
	editing bool
	fields  []mcpFormField
	// pairs is the pair row being edited, when one is open in place of the form.
	pairs    *mcpPairsModel
	focus    int
	err      string
	taken    map[string]bool
	width    int
	height   int
	canceled bool

	name   string
	config contracts.MCPServerConfig
}

// runMCPForm collects one server. seed is the server an edit started from, and
// nil for a new one; taken is the names an add cannot use.
func runMCPForm(ctx context.Context, in io.Reader, out io.Writer, seed *glue.MCPServerView, taken map[string]bool) (string, contracts.MCPServerConfig, error) {
	model := newMCPFormModel(seed, taken)
	result, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return "", contracts.MCPServerConfig{}, err
	}
	form := result.(*mcpFormModel)
	if form.canceled {
		return "", contracts.MCPServerConfig{}, errMCPFormCanceled
	}
	return form.name, form.config, nil
}

func newMCPFormModel(seed *glue.MCPServerView, taken map[string]bool) *mcpFormModel {
	editing := seed != nil
	transport := "streamable-http"
	oauth, credential := "no", string(contracts.MCPAuthNone)
	timeout, target, command, args := "", "", "", ""
	if editing {
		transport = seed.Transport
		if seed.Auth == contracts.MCPAuthOAuth {
			oauth = "yes"
		} else {
			credential = string(seed.Auth)
		}
		if seed.TimeoutSeconds > 0 {
			timeout = strconv.Itoa(seed.TimeoutSeconds)
		}
		if transport == "stdio" {
			command, args = seed.Command, strings.Join(seed.Args, " ")
		} else {
			target = seed.URL
		}
	}

	// An edit cannot read a credential back, so a credential row it cannot fill
	// starts empty and says so: the engine keeps what it has when the row
	// arrives empty.
	keep := ""
	if editing {
		keep = "empty keeps stored"
	}
	fields := []mcpFormField{
		mcpChoiceField(mcpFieldTransport, "Transport", []string{"streamable-http", "stdio"}, transport, editing),
		mcpTextField(mcpFieldName, "Name", "letters, digits, _ or -", seedName(seed), editing),
		mcpTextField(mcpFieldURL, "URL", "https://host/mcp", target, false),
		mcpChoiceField(mcpFieldOAuth, "OAuth", []string{"no", "yes"}, oauth, editing),
		mcpChoiceField(mcpFieldCredential, "Credential", []string{
			string(contracts.MCPAuthNone),
			string(contracts.MCPAuthBearer),
			string(contracts.MCPAuthHeaders),
		}, credential, editing),
		mcpSecretField(mcpFieldBearer, "Bearer token", keep),
		mcpPairsField(mcpFieldHeaders, "Headers", "Name=Value", nil),
		mcpTextField(mcpFieldTimeout, "Timeout", "seconds, empty means 60", timeout, false),
		mcpTextField(mcpFieldCommand, "Command", "the executable, without a shell", command, false),
		mcpTextField(mcpFieldArgs, "Arguments", "space separated", args, false),
		mcpPairsField(mcpFieldEnv, "Environment", "NAME=value", nil),
	}
	model := &mcpFormModel{editing: editing, fields: fields, taken: taken}
	model.setFocus(model.firstVisible())
	return model
}

func seedName(seed *glue.MCPServerView) string {
	if seed == nil {
		return ""
	}
	return seed.Name
}

func mcpTextField(key, label, hint, value string, readonly bool) mcpFormField {
	return mcpFormField{key: key, label: label, hint: hint, kind: mcpFormText, input: newMCPFormInput(value, hint, false), readonly: readonly}
}

func mcpSecretField(key, label, hint string) mcpFormField {
	return mcpFormField{key: key, label: label, hint: hint, kind: mcpFormText, input: newMCPFormInput("", hint, true)}
}

func mcpChoiceField(key, label string, choices []string, value string, readonly bool) mcpFormField {
	field := mcpFormField{key: key, label: label, kind: mcpFormChoice, choices: choices, readonly: readonly}
	for i, choice := range choices {
		if choice == value {
			field.choice = i
		}
	}
	return field
}

func newMCPFormInput(value, placeholder string, masked bool) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.SetValue(value)
	input.CursorEnd()
	input.SetWidth(mcpFormInputWidth)
	input.Placeholder = clipMCPHint(placeholder)
	if masked {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	// Both states carry the panel background: the field is a cell of the form,
	// and the terminal's own background would show through it otherwise.
	textStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text)
	placeholderStyle := lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Muted)
	styles := input.Styles()
	styles.Focused.Text = textStyle
	styles.Focused.Placeholder = placeholderStyle
	styles.Focused.Suggestion = placeholderStyle
	styles.Blurred = styles.Focused
	styles.Cursor.Color = everforest.Green
	input.SetStyles(styles)
	return input
}

// clipMCPHint keeps a row's hint inside the field it stands in for: a longer
// placeholder would paint past the row and stretch the whole panel.
func clipMCPHint(hint string) string {
	runes := []rune(hint)
	if len(runes) <= mcpFormInputWidth-1 {
		return hint
	}
	return string(runes[:mcpFormInputWidth-2]) + "…"
}

// mcpFormStyles are the screen styles on the panel's own background. Every
// piece of a row — label, arrows, separators, the padding between options — is
// painted with it: a fragment left unstyled drops the panel's background at the
// next inline reset and reads as a hole in the form.
func mcpFormStyles() harnessStyles {
	styles := newHarnessStyles()
	surface := everforest.Surface
	styles.screenBody = styles.screenBody.Background(surface)
	styles.muted = styles.muted.Background(surface)
	styles.active = styles.active.Background(surface)
	styles.inactive = styles.inactive.Background(surface)
	styles.error = styles.error.Background(surface)
	styles.screenRow = styles.screenRow.Background(surface)
	// The panel's own frame is part of the panel: without a background of its
	// own, the border column drops to the terminal's.
	styles.menu = styles.menu.BorderBackground(surface)
	return styles
}

// mcpFormGap is the panel background on its own: what the parts of a row that
// carry no colour of their own are painted with.
func mcpFormGap() lipgloss.Style {
	return lipgloss.NewStyle().Background(everforest.Surface)
}

// Init implements tea.Model.
func (m *mcpFormModel) Init() tea.Cmd { return nil }

// Update implements tea.Model. The form owns the keyboard while it is open, so
// the arrows move between rows and change the rows that have alternatives.
func (m *mcpFormModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	// A pair row opens its entries in place of the form: it owns the keyboard
	// until it is closed, and leaving it with ctrl+c leaves the whole form.
	if m.pairs != nil {
		updated, cmd := m.pairs.Update(message)
		pairs := updated.(*mcpPairsModel)
		m.pairs = pairs
		switch {
		case pairs.cancelled:
			m.canceled = true
			return m, tea.Quit
		case pairs.closed:
			m.pairs = nil
		}
		return m, cmd
	}
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.PasteMsg:
		if field := m.focusedText(); field != nil {
			var cmd tea.Cmd
			field.input, cmd = field.input.Update(msg)
			return m, cmd
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *mcpFormModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c", "esc":
		m.canceled = true
		return m, tea.Quit
	case "tab", "down":
		m.moveFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.moveFocus(-1)
		return m, nil
	}

	field := &m.fields[m.focus]
	// enter belongs to the row it is on: a row of entries opens them, and every
	// other row saves the form.
	if key == "enter" {
		if field.kind == mcpFormPairs && !field.readonly {
			m.openPairs(field)
			return m, nil
		}
		if m.submit() {
			return m, tea.Quit
		}
		return m, nil
	}
	if field.readonly {
		return m, nil
	}
	switch field.kind {
	case mcpFormPairs:
		if key == "right" || key == "l" {
			m.openPairs(field)
		}
		return m, nil
	case mcpFormChoice:
		switch key {
		case "left", "h":
			field.choice = (field.choice - 1 + len(field.choices)) % len(field.choices)
		case "right", "l", "space", " ":
			field.choice = (field.choice + 1) % len(field.choices)
		}
		m.err = ""
		return m, nil
	}

	// Everything else belongs to the focused row: the text input owns the
	// caret, the selection and the editing keys.
	var cmd tea.Cmd
	field.input, cmd = field.input.Update(msg)
	m.err = ""
	return m, cmd
}

// View implements tea.Model.
func (m *mcpFormModel) View() tea.View {
	if m.pairs != nil {
		return m.pairs.View()
	}
	return mcpScreen(m.lines(), m.width, m.height, "MCP server", 0)
}

// lines is the form's content, one string per row. A screen opened from the form
// measures itself against it, so the two are the same width and the panel does
// not jump when one replaces the other.
func (m *mcpFormModel) lines() []string {
	styles := mcpFormStyles()
	body := "register a server the harness connects to"
	if m.editing {
		body = "editing " + m.text(mcpFieldName) + ": the name, the transport and the OAuth choice are fixed"
	}
	lines := []string{styles.heading("MCP server", everforest.Surface), styles.screenBody.Render(body), ""}
	for i := range m.fields {
		if !m.visible(&m.fields[i]) {
			continue
		}
		lines = append(lines, m.row(&m.fields[i], i == m.focus))
	}
	if m.err != "" {
		lines = append(lines, "", styles.error.Render(m.err))
	}
	help := "tab/↑↓ move • ←/→ change • enter save • esc cancel"
	if field := &m.fields[m.focus]; field.kind == mcpFormPairs && !field.readonly {
		// enter does not save here: this row opens what it holds.
		help = "tab/↑↓ move • enter open entries • esc cancel"
	}
	return append(lines, "", styles.muted.Render(help))
}

// panelWidth is how wide the form's panel is, so a screen opened from it can be
// the same width.
func (m *mcpFormModel) panelWidth() int {
	widest := 0
	for _, line := range m.lines() {
		widest = max(widest, lipgloss.Width(line))
	}
	return widest
}

// row renders one row: a marker for the row that owns the keyboard, its label,
// then its value — or its alternatives, when it is a row to choose from.
func (m *mcpFormModel) row(field *mcpFormField, focused bool) string {
	styles := mcpFormStyles()
	marker, label := "  ", styles.inactive
	if focused {
		marker, label = "▸ ", styles.active
	}
	// The marker is part of the label cell, so every row's value starts at the
	// same column whether or not the row holds the keyboard.
	line := label.Render(marker + fmt.Sprintf("%-13s", field.label))
	switch {
	case field.kind == mcpFormChoice:
		line += mcpChoiceRow(field)
	case field.kind == mcpFormPairs:
		if summary := mcpPairsSummary(field); summary != "" {
			line += styles.screenRow.Render(summary)
		} else {
			line += styles.muted.Render(field.hint)
		}
	case field.readonly:
		line += styles.screenRow.Render(field.input.Value())
	case focused:
		// The widget draws itself only while it holds the keyboard: blurred it
		// still paints its cursor cell, in a style of its own with no
		// background, which leaves an unpainted cell after the value.
		line += field.input.View()
	default:
		if value := field.input.Value(); value != "" {
			line += styles.screenRow.Render(value)
		} else {
			line += styles.muted.Render(field.hint)
		}
	}
	return line
}

// openPairs opens a pair row's entries. The screen inherits the form's size: the
// terminal only reports its size when it changes, so a screen opened afterwards
// would otherwise have none, and would draw itself in the corner instead of
// where the form was.
func (m *mcpFormModel) openPairs(field *mcpFormField) {
	pairs := newMCPPairsModel(field)
	pairs.width, pairs.height = m.width, m.height
	pairs.minWidth = m.panelWidth()
	m.pairs = pairs
}

// mcpScreen frames one screen of the form: the panel, centred when the terminal
// size is known, in the alternate screen.
func mcpScreen(lines []string, width, height int, title string, minWidth int) tea.View {
	if minWidth > 0 {
		lines = mcpPadLines(lines, minWidth)
	}
	content := mcpFormStyles().menu.Padding(1, 2).Render(strings.Join(lines, "\n"))
	if width > 0 && height > 0 {
		content = lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = appName + " · " + title
	view.BackgroundColor = themeBackground()
	view.ForegroundColor = themeForeground()
	return view
}

// mcpPadLines pads every line to the same width, so a shorter screen still draws
// the panel its caller drew. The padding is painted with the panel's background:
// bare spaces would leave the terminal's own showing as a band across the row.
func mcpPadLines(lines []string, width int) []string {
	padded := make([]string, 0, len(lines))
	for _, line := range lines {
		if gap := width - lipgloss.Width(line); gap > 0 {
			line += mcpFormGap().Render(strings.Repeat(" ", gap))
		}
		padded = append(padded, line)
	}
	return padded
}

// mcpChoiceRow renders every alternative of a choice row, with the one it holds
// picked out: the form is where a server's authentication is chosen, so the
// options have to be on the screen, not behind enough arrow presses.
func mcpChoiceRow(field *mcpFormField) string {
	styles := mcpFormStyles()
	gap := mcpFormGap()
	selected := lipgloss.NewStyle().Background(everforest.SelectionBg).Foreground(everforest.Yellow).Bold(true)
	parts := make([]string, 0, len(field.choices))
	for i, choice := range field.choices {
		text := mcpChoiceLabel(choice)
		if i == field.choice {
			parts = append(parts, selected.Render(" "+text+" "))
			continue
		}
		parts = append(parts, styles.muted.Render(" "+text+" "))
	}
	return gap.Render("◀") + strings.Join(parts, gap.Render(" ")) + gap.Render("▶")
}

func mcpChoiceLabel(value string) string {
	switch value {
	case "streamable-http":
		return "streamable-http (remote URL)"
	case "stdio":
		return "stdio (local command)"
	case "yes":
		return "yes, browser login"
	case "no":
		return "no"
	case string(contracts.MCPAuthBearer):
		return "bearer token"
	case string(contracts.MCPAuthHeaders):
		return "custom headers"
	}
	return value
}

// visible reports whether a row belongs to the answers the form has so far.
// Every row stays in the list, so a value survives the operator cycling past it
// and back.
func (m *mcpFormModel) visible(field *mcpFormField) bool {
	remote := m.choice(mcpFieldTransport) == "streamable-http"
	oauth := m.choice(mcpFieldOAuth) == "yes"
	credential := m.choice(mcpFieldCredential)
	switch field.key {
	case mcpFieldURL, mcpFieldTimeout, mcpFieldOAuth:
		return remote
	case mcpFieldCredential:
		// OAuth is the other way to authenticate, so a credential is asked for
		// only when the server does not use it.
		return remote && !oauth
	case mcpFieldBearer:
		return remote && !oauth && credential == string(contracts.MCPAuthBearer)
	case mcpFieldHeaders:
		return remote && !oauth && credential == string(contracts.MCPAuthHeaders)
	case mcpFieldCommand, mcpFieldArgs, mcpFieldEnv:
		return !remote
	}
	return true
}

func (m *mcpFormModel) field(key string) *mcpFormField {
	for i := range m.fields {
		if m.fields[i].key == key {
			return &m.fields[i]
		}
	}
	return nil
}

func (m *mcpFormModel) choice(key string) string {
	field := m.field(key)
	if field == nil || field.kind != mcpFormChoice {
		return ""
	}
	return field.choices[field.choice]
}

func (m *mcpFormModel) text(key string) string {
	field := m.field(key)
	if field == nil {
		return ""
	}
	return field.input.Value()
}

func (m *mcpFormModel) focusedText() *mcpFormField {
	if m.focus < 0 || m.focus >= len(m.fields) {
		return nil
	}
	field := &m.fields[m.focus]
	if field.kind != mcpFormText || field.readonly {
		return nil
	}
	return field
}

func (m *mcpFormModel) firstVisible() int {
	for i := range m.fields {
		if m.visible(&m.fields[i]) {
			return i
		}
	}
	return 0
}

func (m *mcpFormModel) setFocus(index int) {
	m.focus = index
	for i := range m.fields {
		field := &m.fields[i]
		if i == index && field.kind == mcpFormText && !field.readonly {
			field.input.Focus()
			continue
		}
		field.input.Blur()
	}
}

func (m *mcpFormModel) moveFocus(delta int) {
	m.err = ""
	total := len(m.fields)
	for step := 1; step <= total; step++ {
		index := ((m.focus+delta*step)%total + total) % total
		if m.visible(&m.fields[index]) {
			m.setFocus(index)
			return
		}
	}
}

// submit turns the rows into one server. A rejected form keeps the operator in
// it, with the reason on the row it came from.
func (m *mcpFormModel) submit() bool {
	name := strings.TrimSpace(m.text(mcpFieldName))
	if name == "" {
		m.err = "a server name is required"
		return false
	}
	if m.taken[name] {
		m.err = fmt.Sprintf("a server named %s is already configured", name)
		return false
	}
	config, err := m.build()
	if err != nil {
		m.err = err.Error()
		return false
	}
	if err := store.ValidateMCPServer(name, config); err != nil {
		m.err = err.Error()
		return false
	}
	m.name, m.config = name, config
	return true
}

func (m *mcpFormModel) build() (contracts.MCPServerConfig, error) {
	config := contracts.MCPServerConfig{Transport: m.choice(mcpFieldTransport)}
	if config.Transport == "streamable-http" {
		config.URL = strings.TrimSpace(m.text(mcpFieldURL))
		if timeout := strings.TrimSpace(m.text(mcpFieldTimeout)); timeout != "" {
			seconds, err := strconv.Atoi(timeout)
			if err != nil || seconds < 0 {
				return config, errors.New("the timeout must be a non-negative whole number of seconds")
			}
			config.TimeoutSeconds = seconds
		}
		if m.choice(mcpFieldOAuth) == "yes" {
			// An empty block is what the engine authorizes: it discovers the
			// authorization server and registers the client with PKCE. A
			// preregistered client is set in tools.json.
			config.OAuth = &contracts.MCPOAuthConfig{}
			return config, nil
		}
		switch m.choice(mcpFieldCredential) {
		case string(contracts.MCPAuthBearer):
			config.BearerToken = strings.TrimSpace(m.text(mcpFieldBearer))
		case string(contracts.MCPAuthHeaders):
			config.Headers = mcpPairsMap(m.field(mcpFieldHeaders))
		}
		return config, nil
	}

	config.Command = strings.TrimSpace(m.text(mcpFieldCommand))
	config.Args = strings.Fields(m.text(mcpFieldArgs))
	config.Env = mcpPairsMap(m.field(mcpFieldEnv))
	return config, nil
}
