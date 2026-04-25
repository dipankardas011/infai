package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/model"
)

type fieldKind int

const (
	fieldText   fieldKind = iota
	fieldInt
	fieldFloat
	fieldBool
	fieldSelect
)

var cacheTypeOptions = []string{"(omit)", "f16", "bf16", "q8_0", "q4_0", "q4_1", "q5_0", "q5_1", "q6_K"}

type formField struct {
	label    string
	kind     fieldKind
	input    textinput.Model
	boolVal  bool
	optional bool
	disabled bool
	// fieldSelect only
	options []string
	selIdx  int
}

// ProfileEditModel is screen 3.
type ProfileEditModel struct {
	fields      []formField
	focused     int
	modelEntry  model.ModelEntry
	editingID   int64
	modelID     int64
	errMsg      string
	viewOffset  int
	visibleRows int
	initialized bool
}

func newTextInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	return ti
}

func newSelectField(label string, options []string, currentVal *string) formField {
	idx := 0
	if currentVal != nil {
		for i, o := range options {
			if o == *currentVal {
				idx = i
				break
			}
		}
	}
	return formField{label: label, kind: fieldSelect, options: options, selIdx: idx}
}

func NewProfileEditModel(m model.ModelEntry, p *model.Profile, w, h int) ProfileEditModel {
	hasMmproj := m.MmprojPath != ""

	var cacheKVal, cacheVVal *string
	if p != nil {
		cacheKVal = p.CacheTypeK
		cacheVVal = p.CacheTypeV
	}

	fields := []formField{
		{label: "Name", kind: fieldText, input: newTextInput("e.g. text-only")},
		{label: "Port", kind: fieldInt, input: newTextInput("8000")},
		{label: "Host", kind: fieldText, input: newTextInput("0.0.0.0")},
		{label: "Context Size", kind: fieldInt, input: newTextInput("65536")},
		{label: "NGL", kind: fieldText, input: newTextInput("auto")},
		{label: "Batch Size", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "UBatch Size", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		newSelectField("Cache Type K", cacheTypeOptions, cacheKVal),
		newSelectField("Cache Type V", cacheTypeOptions, cacheVVal),
		{label: "Flash Attn", kind: fieldBool},
		{label: "Jinja", kind: fieldBool},
		{label: "Temperature", kind: fieldFloat, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Reasoning Budget", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Top P", kind: fieldFloat, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Top K", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "No KV Offload", kind: fieldBool},
		{label: "Use Mmproj", kind: fieldBool, disabled: !hasMmproj},
		{label: "Extra Flags", kind: fieldText, input: newTextInput("(empty=omit)"), optional: true},
	}

	em := ProfileEditModel{
		fields:      fields,
		modelEntry:  m,
		modelID:     m.ID,
		visibleRows: h - 8,
	}

	set := func(label, val string) {
		for i := range em.fields {
			if em.fields[i].label == label && em.fields[i].kind != fieldSelect {
				em.fields[i].input.SetValue(val)
				return
			}
		}
	}
	setBool := func(label string, val bool) {
		for i := range em.fields {
			if em.fields[i].label == label {
				em.fields[i].boolVal = val
				return
			}
		}
	}

	if p != nil {
		em.editingID = p.ID
		em.modelID = p.ModelID
		set("Name", p.Name)
		set("Port", strconv.Itoa(p.Port))
		set("Host", p.Host)
		set("Context Size", strconv.Itoa(p.ContextSize))
		set("NGL", p.NGL)
		if p.BatchSize != nil {
			set("Batch Size", strconv.Itoa(*p.BatchSize))
		}
		if p.UBatchSize != nil {
			set("UBatch Size", strconv.Itoa(*p.UBatchSize))
		}
		setBool("Flash Attn", p.FlashAttn)
		setBool("Jinja", p.Jinja)
		if p.Temperature != nil {
			set("Temperature", strconv.FormatFloat(*p.Temperature, 'f', -1, 64))
		}
		if p.ReasoningBudget != nil {
			set("Reasoning Budget", strconv.Itoa(*p.ReasoningBudget))
		}
		if p.TopP != nil {
			set("Top P", strconv.FormatFloat(*p.TopP, 'f', -1, 64))
		}
		if p.TopK != nil {
			set("Top K", strconv.Itoa(*p.TopK))
		}
		setBool("No KV Offload", p.NoKVOffload)
		setBool("Use Mmproj", p.UseMmproj && hasMmproj)
		set("Extra Flags", p.ExtraFlags)
	} else {
		set("Port", "8000")
		set("Host", "0.0.0.0")
		set("Context Size", "65536")
		set("NGL", "auto")
	}

	if em.fields[0].kind != fieldBool && em.fields[0].kind != fieldSelect {
		em.fields[0].input.Focus()
	}
	em.initialized = true
	return em
}

func (em ProfileEditModel) SetSize(w, h int) ProfileEditModel {
	if !em.initialized {
		return em
	}
	em.visibleRows = h - 8
	return em
}

func (em ProfileEditModel) Update(msg tea.Msg) (ProfileEditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f := &em.fields[em.focused]
		switch msg.String() {
		case "tab", "down":
			if f.kind != fieldBool && f.kind != fieldSelect {
				f.input.Blur()
			}
			em.focused = (em.focused + 1) % len(em.fields)
			nf := &em.fields[em.focused]
			if nf.kind != fieldBool && nf.kind != fieldSelect {
				nf.input.Focus()
			}
			em.scrollTo(em.focused)
			return em, textinput.Blink

		case "shift+tab", "up":
			if f.kind != fieldBool && f.kind != fieldSelect {
				f.input.Blur()
			}
			em.focused = (em.focused - 1 + len(em.fields)) % len(em.fields)
			nf := &em.fields[em.focused]
			if nf.kind != fieldBool && nf.kind != fieldSelect {
				nf.input.Focus()
			}
			em.scrollTo(em.focused)
			return em, textinput.Blink

		case " ", "enter":
			if f.kind == fieldBool && !f.disabled {
				f.boolVal = !f.boolVal
				return em, nil
			}

		case "left":
			if f.kind == fieldSelect {
				f.selIdx = (f.selIdx - 1 + len(f.options)) % len(f.options)
				return em, nil
			}

		case "right":
			if f.kind == fieldSelect {
				f.selIdx = (f.selIdx + 1) % len(f.options)
				return em, nil
			}
		}
	}

	f := em.fields[em.focused]
	if f.kind != fieldBool && f.kind != fieldSelect {
		var cmd tea.Cmd
		em.fields[em.focused].input, cmd = em.fields[em.focused].input.Update(msg)
		return em, cmd
	}
	return em, nil
}

func (em *ProfileEditModel) scrollTo(idx int) {
	if em.visibleRows <= 0 {
		return
	}
	if idx < em.viewOffset {
		em.viewOffset = idx
	} else if idx >= em.viewOffset+em.visibleRows {
		em.viewOffset = idx - em.visibleRows + 1
	}
}

func (em ProfileEditModel) View() string {
	t := ActiveTheme
	title := styleTitle.Render("Edit Profile — " + em.modelEntry.DisplayName)
	var rows []string

	end := em.viewOffset + em.visibleRows
	if end > len(em.fields) {
		end = len(em.fields)
	}

	for i := em.viewOffset; i < end; i++ {
		f := em.fields[i]
		label := styleLabel.Render(f.label)
		focused := i == em.focused

		var value string
		switch f.kind {
		case fieldBool:
			box := "[ ]"
			if f.boolVal {
				box = styleSuccess.Render("[✓]")
			}
			if f.disabled {
				value = styleMuted.Render("[ ] (no mmproj)")
			} else if focused {
				value = box + styleMuted.Render("  space to toggle")
			} else {
				value = box
			}

		case fieldSelect:
			prev := styleMuted.Render("◀")
			next := styleMuted.Render("▶")
			opt := f.options[f.selIdx]
			optStr := ""
			if opt == "(omit)" {
				optStr = styleMuted.Render(opt)
			} else {
				optStr = lipgloss.NewStyle().Foreground(t.Primary).Render(opt)
			}
			if focused {
				prev = styleKey.Render("◀")
				next = styleKey.Render("▶")
				value = prev + " " + optStr + " " + next + styleMuted.Render("  ←/→")
			} else {
				value = prev + " " + optStr + " " + next
			}

		default:
			value = f.input.View()
		}

		prefix := "  "
		if focused {
			prefix = lipgloss.NewStyle().Foreground(t.Primary).Render("▶ ")
		}
		rows = append(rows, fmt.Sprintf("%s%s  %s", prefix, label, value))
	}

	scrollHint := ""
	if len(em.fields) > em.visibleRows {
		scrollHint = " " + styleMuted.Render(fmt.Sprintf("(%d/%d)", em.focused+1, len(em.fields)))
	}

	errLine := ""
	if em.errMsg != "" {
		errLine = "\n" + styleError.Render("  ✗ "+em.errMsg)
	}

	help := styleHelp.Render("tab/↑↓: navigate  ←/→: cycle select  space: toggle  ctrl+s: save  esc: discard")
	return title + "\n\n" + strings.Join(rows, "\n") + scrollHint + errLine + "\n\n" + help
}

// ToProfile extracts and validates form state into a Profile.
func (em ProfileEditModel) ToProfile() (model.Profile, error) {
	get := func(label string) string {
		for _, f := range em.fields {
			if f.label == label && f.kind != fieldSelect && f.kind != fieldBool {
				return strings.TrimSpace(f.input.Value())
			}
		}
		return ""
	}
	getBool := func(label string) bool {
		for _, f := range em.fields {
			if f.label == label {
				return f.boolVal
			}
		}
		return false
	}
	getSelect := func(label string) *string {
		for _, f := range em.fields {
			if f.label == label && f.kind == fieldSelect {
				if f.options[f.selIdx] == "(omit)" {
					return nil
				}
				v := f.options[f.selIdx]
				return &v
			}
		}
		return nil
	}
	optInt := func(label string) (*int, error) {
		v := get(label)
		if v == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer", label)
		}
		return &n, nil
	}
	optFloat := func(label string) (*float64, error) {
		v := get(label)
		if v == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be a number", label)
		}
		return &f, nil
	}

	name := get("Name")
	if name == "" {
		return model.Profile{}, fmt.Errorf("Name is required")
	}
	port, err := strconv.Atoi(get("Port"))
	if err != nil || port < 1 || port > 65535 {
		return model.Profile{}, fmt.Errorf("Port must be 1-65535")
	}
	ctx, err := strconv.Atoi(get("Context Size"))
	if err != nil || ctx <= 0 {
		return model.Profile{}, fmt.Errorf("Context Size must be > 0")
	}
	ngl := get("NGL")
	if ngl == "" {
		ngl = "auto"
	}
	batchSize, err := optInt("Batch Size")
	if err != nil {
		return model.Profile{}, err
	}
	ubatchSize, err := optInt("UBatch Size")
	if err != nil {
		return model.Profile{}, err
	}
	temp, err := optFloat("Temperature")
	if err != nil {
		return model.Profile{}, err
	}
	rb, err := optInt("Reasoning Budget")
	if err != nil {
		return model.Profile{}, err
	}
	topP, err := optFloat("Top P")
	if err != nil {
		return model.Profile{}, err
	}
	topK, err := optInt("Top K")
	if err != nil {
		return model.Profile{}, err
	}

	return model.Profile{
		ID:              em.editingID,
		ModelID:         em.modelID,
		Name:            name,
		Port:            port,
		Host:            get("Host"),
		ContextSize:     ctx,
		NGL:             ngl,
		BatchSize:       batchSize,
		UBatchSize:      ubatchSize,
		CacheTypeK:      getSelect("Cache Type K"),
		CacheTypeV:      getSelect("Cache Type V"),
		FlashAttn:       getBool("Flash Attn"),
		Jinja:           getBool("Jinja"),
		Temperature:     temp,
		ReasoningBudget: rb,
		TopP:            topP,
		TopK:            topK,
		NoKVOffload:     getBool("No KV Offload"),
		UseMmproj:       getBool("Use Mmproj"),
		ExtraFlags:      get("Extra Flags"),
	}, nil
}
