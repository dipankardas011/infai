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
	fieldText fieldKind = iota
	fieldInt
	fieldFloat
	fieldBool
	fieldSelect
)

var cacheTypeOptions = []string{"(omit)", "f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}

type formField struct {
	label    string
	kind     fieldKind
	input    textinput.Model
	boolVal  bool
	optional bool
	disabled bool
	// fieldSelect only
	options      []string
	optionValues []string
	selIdx       int
}

// ProfileEditModel is screen 3.
type ProfileEditModel struct {
	fields      []formField
	focused     int
	modelEntry  model.ModelEntry
	engines     []model.InferenceEngine
	editingID   int64
	modelID     int64
	errMsg      string
	viewOffset  int
	visibleRows int
	width       int
	height      int
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

func newEngineSelectField(engines []model.InferenceEngine, currentID string) formField {
	options := make([]string, 0, len(engines))
	values := make([]string, 0, len(engines))
	idx := 0
	for i, e := range engines {
		options = append(options, e.Name)
		values = append(values, e.ID)
		if e.ID == currentID {
			idx = i
		}
	}
	return formField{label: "Inference Engine", kind: fieldSelect, options: options, optionValues: values, selIdx: idx}
}

func NewProfileEditModel(m model.ModelEntry, engines []model.InferenceEngine, p *model.Profile, w, h int) ProfileEditModel {
	hasMmproj := m.MmprojPath != ""

	var cacheKVal, cacheVVal *string
	if p != nil {
		cacheKVal = p.CacheTypeK
		cacheVVal = p.CacheTypeV
	}

	currentEngineID := ""
	if p != nil {
		currentEngineID = p.InferenceEngineID
	}

	fields := []formField{
		{label: "Name", kind: fieldText, input: newTextInput("e.g. text-only")},
		newEngineSelectField(engines, currentEngineID),
		{label: "Port", kind: fieldInt, input: newTextInput("8000")},
		{label: "Host", kind: fieldText, input: newTextInput("0.0.0.0")},
		{label: "Context Size", kind: fieldInt, input: newTextInput("64")},
		newSelectField("Context Unit", []string{"None", "K", "M"}, nil),
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
		engines:     engines,
		modelID:     m.ID,
		visibleRows: h - 12,
		width:       w,
		height:      h,
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
	setSelect := func(label, val string) {
		for i := range em.fields {
			if em.fields[i].label == label && em.fields[i].kind == fieldSelect {
				for j, o := range em.fields[i].options {
					if o == val {
						em.fields[i].selIdx = j
						return
					}
				}
			}
		}
	}

	if p != nil {
		em.editingID = p.ID
		em.modelID = p.ModelID
		set("Name", p.Name)
		set("Port", strconv.Itoa(p.Port))
		set("Host", p.Host)

		ctxVal := p.ContextSize
		ctxUnit := "None"
		if ctxVal > 0 {
			if ctxVal%(1024*1024) == 0 {
				ctxVal /= (1024 * 1024)
				ctxUnit = "M"
			} else if ctxVal%1024 == 0 {
				ctxVal /= 1024
				ctxUnit = "K"
			}
		}
		set("Context Size", strconv.Itoa(ctxVal))
		setSelect("Context Unit", ctxUnit)

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
		set("Context Size", "64")
		setSelect("Context Unit", "K")
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
	em.width = w
	em.height = h
	em.visibleRows = em.computeVisibleRows()
	em.scrollTo(em.focused)
	return em
}

func (em ProfileEditModel) Update(msg tea.Msg) (ProfileEditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f := &em.fields[em.focused]
		switch msg.String() {
		case "tab":
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

		case "shift+tab":
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
			if f.kind == fieldSelect && len(f.options) > 0 {
				f.selIdx = (f.selIdx - 1 + len(f.options)) % len(f.options)
				return em, nil
			}

		case "right":
			if f.kind == fieldSelect && len(f.options) > 0 {
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

func (em ProfileEditModel) computeVisibleRows() int {
	// AppModel has already reserved global header/footer/help. This screen owns
	// em.height lines. Border+padding consume 4 lines. Inside the box we keep:
	// title+blank (2), blank+help (2), optional error (1). Everything else is
	// the scrollable field list.
	overhead := 8
	if em.errMsg != "" {
		overhead++
	}
	rows := em.height - overhead
	if rows < 1 {
		rows = 1
	}
	if rows > len(em.fields) {
		rows = len(em.fields)
	}
	return rows
}

func (em *ProfileEditModel) scrollTo(idx int) {
	if em.visibleRows <= 0 {
		em.visibleRows = em.computeVisibleRows()
	}
	if idx < em.viewOffset {
		em.viewOffset = idx
	} else if idx >= em.viewOffset+em.visibleRows {
		em.viewOffset = idx - em.visibleRows + 1
	}
	if em.viewOffset < 0 {
		em.viewOffset = 0
	}
	maxOffset := len(em.fields) - em.visibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if em.viewOffset > maxOffset {
		em.viewOffset = maxOffset
	}
}

func (em ProfileEditModel) View() string {
	t := ActiveTheme
	visibleRows := em.computeVisibleRows()
	if visibleRows != em.visibleRows {
		em.visibleRows = visibleRows
		em.scrollTo(em.focused)
	}

	boxW := em.width - 4
	if boxW > 96 {
		boxW = 96
	}
	if boxW < 44 {
		boxW = max(em.width-2, 20)
	}
	innerW := max(boxW-6, 20) // border + horizontal padding
	labelW := 18
	if innerW < 58 {
		labelW = 14
	}
	valueW := innerW - labelW - 6
	if valueW < 10 {
		valueW = 10
	}

	title := styleTitle.Render("Edit Profile — " + truncatePath(em.modelEntry.DisplayName, max(innerW-18, 12)))
	var rows []string

	end := min(em.viewOffset+visibleRows, len(em.fields))

	for i := em.viewOffset; i < end; i++ {
		f := em.fields[i]
		label := lipgloss.NewStyle().Foreground(t.Muted).Width(labelW).Align(lipgloss.Right).Render(f.label)
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
			opt := "(none)"
			if len(f.options) > 0 {
				opt = f.options[f.selIdx]
			}
			optStr := ""
			if opt == "(omit)" || opt == "(none)" {
				optStr = styleMuted.Render(opt)
			} else {
				optStr = lipgloss.NewStyle().Foreground(t.Primary).Render(opt)
			}
			if focused {
				value = optStr + styleMuted.Render("  ←/→ to change")
			} else {
				value = optStr
			}

		default:
			// Keep long fields (especially Extra Flags) usable at any terminal width.
			f.input.Width = valueW
			value = f.input.View()
		}

		prefix := "  "
		if focused {
			prefix = lipgloss.NewStyle().Foreground(t.Primary).Render("▶ ")
		}
		rows = append(rows, fmt.Sprintf("%s%s  %s", prefix, label, value))
	}

	scrollHint := ""
	if len(em.fields) > visibleRows {
		scrollHint = " " + styleMuted.Render(fmt.Sprintf("field %d/%d", em.focused+1, len(em.fields)))
	}

	errLine := ""
	if em.errMsg != "" {
		errLine = "\n" + styleError.Render("  ✗ "+em.errMsg)
	}

	help := styleHelp.Render("tab/s-tab: navigate  ←/→: cycle select  space: toggle  ctrl+s: save  esc: discard")

	content := title + "\n\n" + strings.Join(rows, "\n") + scrollHint + errLine + "\n\n" + help

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(boxW).
		MaxHeight(max(em.height, 1)).
		Render(content)

	return lipgloss.Place(em.width, em.height, lipgloss.Center, lipgloss.Center, box)
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
				if len(f.options) == 0 {
					return nil
				}
				if f.options[f.selIdx] == "(omit)" {
					return nil
				}
				v := f.options[f.selIdx]
				return &v
			}
		}
		return nil
	}
	getSelectValue := func(label string) string {
		for _, f := range em.fields {
			if f.label == label && f.kind == fieldSelect && len(f.options) > 0 {
				if len(f.optionValues) == len(f.options) {
					return f.optionValues[f.selIdx]
				}
				return f.options[f.selIdx]
			}
		}
		return ""
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
	engineID := getSelectValue("Inference Engine")
	if engineID == "" {
		return model.Profile{}, fmt.Errorf("Inference Engine is required")
	}
	port, err := strconv.Atoi(get("Port"))
	if err != nil || port < 1 || port > 65535 {
		return model.Profile{}, fmt.Errorf("Port must be 1-65535")
	}

	ctxVal, err := strconv.Atoi(get("Context Size"))
	if err != nil || ctxVal <= 0 {
		return model.Profile{}, fmt.Errorf("Context Size must be > 0")
	}
	unitPtr := getSelect("Context Unit")
	ctx := ctxVal
	if unitPtr != nil {
		unit := *unitPtr
		switch unit {
		case "K":
			ctx *= 1024
		case "M":
			ctx *= 1024 * 1024
		}
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
		ID:                em.editingID,
		ModelID:           em.modelID,
		InferenceEngineID: engineID,
		Name:              name,
		Port:              port,
		Host:              get("Host"),
		ContextSize:       ctx,
		NGL:               ngl,
		BatchSize:         batchSize,
		UBatchSize:        ubatchSize,
		CacheTypeK:        getSelect("Cache Type K"),
		CacheTypeV:        getSelect("Cache Type V"),
		FlashAttn:         getBool("Flash Attn"),
		Jinja:             getBool("Jinja"),
		Temperature:       temp,
		ReasoningBudget:   rb,
		TopP:              topP,
		TopK:              topK,
		NoKVOffload:       getBool("No KV Offload"),
		UseMmproj:         getBool("Use Mmproj"),
		ExtraFlags:        get("Extra Flags"),
	}, nil
}
