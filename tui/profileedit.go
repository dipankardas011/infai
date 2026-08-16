package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
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
	label     string
	optionKey string
	kind      fieldKind
	input     textinput.Model
	boolVal   bool
	optional  bool
	disabled  bool
	engine    model.EngineKind
	// fieldSelect only
	options      []string
	optionValues []string
	selIdx       int
}

// ProfileEditModel is screen 3.
type ProfileEditModel struct {
	fields                  []formField
	focused                 int
	modelEntry              model.ModelEntry
	engines                 []model.InferenceEngine
	editingID               int64
	modelID                 int64
	errMsg                  string
	viewOffset              int
	visibleRows             int
	width                   int
	height                  int
	initialized             bool
	suggestionLoading       bool
	recommendation          []string
	requiresAcknowledgement bool
	acknowledged            bool
	fieldErrors             map[string]string
	showAdvanced            bool

	// initial holds the form state at open time so esc can warn about
	// unsaved edits; discardConfirm is the "discard changes?" prompt.
	initial        []string
	discardConfirm bool
}

func (em ProfileEditModel) selectedEngineID() string {
	for _, field := range em.fields {
		if field.label == "Inference Engine" && len(field.optionValues) > 0 && field.selIdx < len(field.optionValues) {
			return field.optionValues[field.selIdx]
		}
	}
	return ""
}

func (em *ProfileEditModel) SetRecommendation(lines []string, requiresAcknowledgement bool) {
	em.suggestionLoading = false
	em.recommendation = append([]string(nil), lines...)
	em.requiresAcknowledgement = requiresAcknowledgement
	em.acknowledged = false
}

func (em *ProfileEditModel) SetFieldErrors(issues []backend.ValidationError) {
	em.fieldErrors = make(map[string]string, len(issues))
	for _, issue := range issues {
		label := map[string]string{
			"name": "Name", "model_id": "Model", "inference_engine_id": "Inference Engine", "inference_engine": "Inference Engine",
			"port": "Port", "host": "Host", "context_size": "Context Size", "ngl": "NGL", "batch_size": "Batch Size", "ubatch_size": "UBatch Size",
			"temperature": "Temperature", "top_p": "Top P", "top_k": "Top K", "reasoning_budget": "Reasoning Budget",
			"gpu_memory_utilization": "GPU Memory Util", "max_num_seqs": "Max Sequences", "max_batched_tokens": "Max Batched Tokens",
			"pipeline_parallel_size": "Pipeline Parallel", "tensor_parallel_size": "Tensor Parallel", "dtype": "vLLM DType", "extra_flags": "Extra Flags",
		}[issue.Field]
		if label == "" {
			label = issue.Field
		}
		em.fieldErrors[label] = issue.Issue
	}
}

// snapshot serializes every field's current value for dirty comparison.
func (em ProfileEditModel) snapshot() []string {
	out := make([]string, 0, len(em.fields))
	for _, f := range em.fields {
		switch f.kind {
		case fieldBool:
			out = append(out, strconv.FormatBool(f.boolVal))
		case fieldSelect:
			out = append(out, strconv.Itoa(f.selIdx))
		default:
			out = append(out, f.input.Value())
		}
	}
	return out
}

// dirty reports whether any field changed since the editor was opened.
func (em ProfileEditModel) dirty() bool {
	current := em.snapshot()
	if len(current) != len(em.initial) {
		return true
	}
	for i := range current {
		if current[i] != em.initial[i] {
			return true
		}
	}
	return false
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
		options = append(options, fmt.Sprintf("%s [%s]", e.Name, e.Kind))
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
		{label: "Port", optionKey: "port", kind: fieldInt, input: newTextInput("8000")},
		{label: "Host", optionKey: "host", kind: fieldText, input: newTextInput("0.0.0.0")},
		{label: "Context Size", optionKey: "context", kind: fieldInt, input: newTextInput("64")},
		newSelectField("Context Unit", []string{"None", "K", "M"}, nil),
		{label: "NGL", optionKey: "gpu_layers", kind: fieldText, input: newTextInput("auto")},
		{label: "Batch Size", optionKey: "batch", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "UBatch Size", optionKey: "ubatch", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		newSelectField("Cache Type K", cacheTypeOptions, cacheKVal),
		newSelectField("Cache Type V", cacheTypeOptions, cacheVVal),
		{label: "Flash Attn", optionKey: "flash_attention", kind: fieldBool},
		{label: "Jinja", optionKey: "jinja", kind: fieldBool},
		{label: "Temperature", optionKey: "temperature", kind: fieldFloat, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Reasoning Budget", optionKey: "reasoning_budget", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Top P", optionKey: "top_p", kind: fieldFloat, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Top K", optionKey: "top_k", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "No KV Offload", optionKey: "kv_offload", kind: fieldBool},
		{label: "Use Mmproj", optionKey: "mmproj", kind: fieldBool, disabled: !hasMmproj},
		{label: "Extra Flags", kind: fieldText, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Served Model Name", optionKey: "served_model_name", kind: fieldText, input: newTextInput("(empty=model name)"), optional: true},
		{label: "GPU Memory Util", optionKey: "gpu_memory_utilization", kind: fieldFloat, input: newTextInput("0.85"), optional: true},
		{label: "Max Sequences", optionKey: "max_num_seqs", kind: fieldInt, input: newTextInput("32"), optional: true},
		{label: "Max Batched Tokens", optionKey: "max_num_batched_tokens", kind: fieldInt, input: newTextInput("4096"), optional: true},
		newSelectField("vLLM DType", []string{"auto", "float16", "bfloat16", "float32"}, nil),
		{label: "Tensor Parallel", optionKey: "tensor_parallel_size", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Pipeline Parallel", optionKey: "pipeline_parallel_size", kind: fieldInt, input: newTextInput("(empty=omit)"), optional: true},
		{label: "Prefix Caching", optionKey: "prefix_caching", kind: fieldBool},
		{label: "Trust Remote Code", optionKey: "trust_remote_code", kind: fieldBool},
	}
	optionKeys := map[string]string{
		"Cache Type K": "cache_type_k", "Cache Type V": "cache_type_v",
		"Extra Flags": "extra_flags", "vLLM DType": "dtype",
	}
	for i := range fields {
		if fields[i].optionKey == "" {
			fields[i].optionKey = optionKeys[fields[i].label]
		}
		for _, option := range backend.OptionCatalog() {
			if option.Key == fields[i].optionKey {
				fields[i].engine = option.Engine
				break
			}
		}
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
		if cfg, err := p.VLLMConfig(); err == nil {
			set("Served Model Name", cfg.ServedModelName)
			if cfg.GPUUtilization != nil {
				set("GPU Memory Util", strconv.FormatFloat(*cfg.GPUUtilization, 'f', -1, 64))
			}
			if cfg.MaxNumSeqs != nil {
				set("Max Sequences", strconv.Itoa(*cfg.MaxNumSeqs))
			}
			if cfg.MaxBatchedTokens != nil {
				set("Max Batched Tokens", strconv.Itoa(*cfg.MaxBatchedTokens))
			}
			if cfg.DType != "" {
				setSelect("vLLM DType", cfg.DType)
			}
			if cfg.TensorParallelSize != nil {
				set("Tensor Parallel", strconv.Itoa(*cfg.TensorParallelSize))
			}
			if cfg.PipelineParallelSize != nil {
				set("Pipeline Parallel", strconv.Itoa(*cfg.PipelineParallelSize))
			}
			setBool("Prefix Caching", cfg.EnablePrefixCaching)
			setBool("Trust Remote Code", cfg.TrustRemoteCode)
		}
	} else {
		set("Port", "8000")
		set("Host", "0.0.0.0")
		set("Context Size", "64")
		setSelect("Context Unit", "K")
		set("NGL", "auto")
		set("GPU Memory Util", "0.85")
		set("Max Sequences", "32")
		set("Max Batched Tokens", "4096")
	}

	if em.fields[0].kind != fieldBool && em.fields[0].kind != fieldSelect {
		em.fields[0].input.Focus()
	}
	em.initial = em.snapshot()
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

func (em *ProfileEditModel) moveFocus(delta int) tea.Cmd {
	f := &em.fields[em.focused]
	if f.kind != fieldBool && f.kind != fieldSelect {
		f.input.Blur()
	}
	for {
		em.focused = (em.focused + delta + len(em.fields)) % len(em.fields)
		if em.fieldVisible(em.fields[em.focused]) {
			break
		}
	}
	nf := &em.fields[em.focused]
	if nf.kind != fieldBool && nf.kind != fieldSelect {
		nf.input.Focus()
	}
	em.scrollTo(em.focused)
	return textinput.Blink
}

func (em ProfileEditModel) Update(msg tea.Msg) (ProfileEditModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f := &em.fields[em.focused]
		switch msg.String() {
		case "ctrl+a":
			em.showAdvanced = !em.showAdvanced
			em.visibleRows = em.computeVisibleRows()
			em.scrollTo(em.focused)
			return em, nil
		case "tab", "down":
			return em, em.moveFocus(1)

		case "shift+tab", "up":
			return em, em.moveFocus(-1)

		case " ":
			if f.kind == fieldBool && !f.disabled {
				f.boolVal = !f.boolVal
				return em, nil
			}

		case "enter":
			if f.kind == fieldBool && !f.disabled {
				f.boolVal = !f.boolVal
				return em, nil
			}
			// On any other field, enter moves on to the next one.
			return em, em.moveFocus(1)

		case "left":
			if f.kind == fieldSelect && len(f.options) > 0 {
				f.selIdx = (f.selIdx - 1 + len(f.options)) % len(f.options)
				em.scrollTo(em.focused)
				return em, nil
			}

		case "right":
			if f.kind == fieldSelect && len(f.options) > 0 {
				f.selIdx = (f.selIdx + 1) % len(f.options)
				em.scrollTo(em.focused)
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
	// AppModel has already reserved global header/footer/help. Reserve the
	// editor's fixed title, separators, and outer box chrome here; only
	// the remaining rows belong to the scrollable field list.
	recommendationHeight := 0
	if em.suggestionLoading {
		recommendationHeight = 3
	} else if len(em.recommendation) > 0 {
		recommendationHeight = min(len(em.recommendation), 3) + 2
	}
	overhead := 9 + recommendationHeight
	if em.errMsg != "" {
		overhead++
	}
	if _, ok := em.focusedOption(); ok {
		overhead += 7 // focused option description box, including wrapped caution
	}
	rows := em.height - overhead
	if rows < 1 {
		rows = 1
	}
	fieldCount := len(em.visibleFieldIndices())
	if rows > fieldCount {
		rows = fieldCount
	}
	return rows
}

func (em *ProfileEditModel) scrollTo(idx int) {
	if em.visibleRows <= 0 {
		em.visibleRows = em.computeVisibleRows()
	}
	visible := em.visibleFieldIndices()
	position := 0
	for i, fieldIndex := range visible {
		if fieldIndex == idx {
			position = i
			break
		}
	}
	if position < em.viewOffset {
		em.viewOffset = position
	} else if position >= em.viewOffset+em.visibleRows {
		em.viewOffset = position - em.visibleRows + 1
	}
	if em.viewOffset < 0 {
		em.viewOffset = 0
	}
	maxOffset := len(visible) - em.visibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if em.viewOffset > maxOffset {
		em.viewOffset = maxOffset
	}
}

func (em ProfileEditModel) selectedEngineKind() model.EngineKind {
	for _, field := range em.fields {
		if field.label != "Inference Engine" || len(field.optionValues) == 0 {
			continue
		}
		id := field.optionValues[field.selIdx]
		for _, engine := range em.engines {
			if engine.ID == id {
				if engine.Kind == "" {
					return model.EngineLlamaCPP
				}
				return engine.Kind
			}
		}
	}
	return model.EngineLlamaCPP
}

func (em ProfileEditModel) fieldVisible(field formField) bool {
	if field.engine != "" && field.engine != em.selectedEngineKind() {
		return false
	}
	if field.optionKey == "" || em.showAdvanced {
		return true
	}
	for _, option := range backend.OptionCatalog() {
		if option.Key == field.optionKey {
			return option.Category == backend.CategoryCommon
		}
	}
	return true
}

func (em ProfileEditModel) focusedOption() (backend.Option, bool) {
	if em.focused < 0 || em.focused >= len(em.fields) {
		return backend.Option{}, false
	}
	key := em.fields[em.focused].optionKey
	if key == "" {
		return backend.Option{}, false
	}
	for _, option := range backend.OptionCatalog() {
		if option.Key == key {
			return option, true
		}
	}
	return backend.Option{}, false
}

func (em ProfileEditModel) visibleFieldIndices() []int {
	indices := make([]int, 0, len(em.fields))
	for i, field := range em.fields {
		if em.fieldVisible(field) {
			indices = append(indices, i)
		}
	}
	return indices
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
	recommendationBlock := ""
	if em.suggestionLoading {
		recommendationBlock = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Padding(0, 1).
			Width(innerW).
			Render(styleMuted.Render("Preparing hardware-aware recommendation..."))
	} else {
		var recommendationRows []string
		summaryCount := min(len(em.recommendation), 3)
		for i, line := range em.recommendation[:summaryCount] {
			lineStyle := styleMuted
			if i == 0 {
				status := strings.ToLower(line)
				switch {
				case strings.Contains(status, "does_not_fit") || strings.Contains(status, "does not fit"):
					lineStyle = styleError
				case strings.Contains(status, "tight"):
					lineStyle = styleWarning
				case strings.Contains(status, "fits"):
					lineStyle = styleSuccess
				}
			}
			recommendationRows = append(recommendationRows, lineStyle.Render(truncateLine(line, innerW-2)))
		}
		if len(recommendationRows) > 0 {
			recommendationBlock = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(t.Primary).
				Padding(0, 1).
				Width(innerW).
				Render(strings.Join(recommendationRows, "\n"))
		}
	}

	visibleFields := em.visibleFieldIndices()
	end := min(em.viewOffset+visibleRows, len(visibleFields))

	for position := em.viewOffset; position < end; position++ {
		i := visibleFields[position]
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
		if issue, ok := em.fieldErrors[f.label]; ok {
			value += " " + styleError.Render("("+issue+")")
		}

		prefix := "  "
		if focused {
			prefix = lipgloss.NewStyle().Foreground(t.Primary).Render("▶ ")
		}
		rows = append(rows, fmt.Sprintf("%s%s  %s", prefix, label, value))
	}

	scrollHint := ""
	if len(visibleFields) > visibleRows {
		focusedPosition := 0
		for i, fieldIndex := range visibleFields {
			if fieldIndex == em.focused {
				focusedPosition = i
				break
			}
		}
		scrollHint = " " + styleMuted.Render(fmt.Sprintf("field %d/%d", focusedPosition+1, len(visibleFields)))
	}

	errLine := ""
	if em.errMsg != "" {
		errLine = "\n" + styleError.Render("  ✗ "+em.errMsg)
	}

	help := styleHelp.Render("↑/↓ or tab: navigate  ←/→: cycle select  space: toggle  ctrl+a: advanced  ctrl+s: save  esc: discard")
	if em.discardConfirm {
		help = lipgloss.NewStyle().Foreground(t.Error).Bold(true).
			Render("unsaved changes — y: discard  n/esc: keep editing")
	}

	content := title
	if recommendationBlock != "" {
		content += "\n\n" + recommendationBlock
	}

	infoBlock := ""
	if option, ok := em.focusedOption(); ok {
		infoW := max(innerW-4, 10)
		infoLines := []string{lipgloss.NewStyle().Width(infoW).Render(styleMuted.Render("[i] " + option.Description))}
		if option.SecurityCaution != "" {
			infoLines = append(infoLines, lipgloss.NewStyle().Width(infoW).Render(styleWarning.Render("Caution: "+option.SecurityCaution)))
		}
		infoBlock = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Primary).
			Padding(0, 1).
			Width(innerW).
			Render(strings.Join(infoLines, "\n"))
	}
	content += "\n\n" + strings.Join(rows, "\n") + "\n" + scrollHint + errLine
	if infoBlock != "" {
		content += "\n\n" + infoBlock
	}
	if em.discardConfirm {
		content += "\n\n" + help
	}

	borderColor := t.Muted
	if em.discardConfirm {
		borderColor = t.Error
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2).
		Width(boxW).
		Height(max(em.height-8, 1)).
		MaxHeight(max(em.height, 1)).
		Render(content)

	return lipgloss.Place(em.width, em.height, lipgloss.Center, lipgloss.Top, box)
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
	engineKind := model.EngineLlamaCPP
	for _, engine := range em.engines {
		if engine.ID == engineID {
			engineKind = engine.Kind
			break
		}
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
	var engineConfig string
	if engineKind == model.EngineVLLM {
		gpuUtil, err := optFloat("GPU Memory Util")
		if err != nil {
			return model.Profile{}, err
		}
		if gpuUtil != nil && (*gpuUtil <= 0 || *gpuUtil > 1) {
			return model.Profile{}, fmt.Errorf("GPU Memory Util must be > 0 and <= 1")
		}
		maxSeqs, err := optInt("Max Sequences")
		if err != nil {
			return model.Profile{}, err
		}
		maxBatched, err := optInt("Max Batched Tokens")
		if err != nil {
			return model.Profile{}, err
		}
		tp, err := optInt("Tensor Parallel")
		if err != nil {
			return model.Profile{}, err
		}
		pp, err := optInt("Pipeline Parallel")
		if err != nil {
			return model.Profile{}, err
		}
		cfg := model.VLLMConfig{
			ServedModelName: get("Served Model Name"), GPUUtilization: gpuUtil,
			MaxNumSeqs: maxSeqs, MaxBatchedTokens: maxBatched,
			TensorParallelSize: tp, PipelineParallelSize: pp,
			EnablePrefixCaching: getBool("Prefix Caching"), TrustRemoteCode: getBool("Trust Remote Code"),
		}
		if dtype := getSelect("vLLM DType"); dtype != nil {
			cfg.DType = *dtype
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return model.Profile{}, fmt.Errorf("encode vLLM configuration: %w", err)
		}
		engineConfig = string(raw)
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
		EngineConfig:      engineConfig,
	}, nil
}

func truncateLine(value string, width int) string {
	if width <= 3 || len(value) <= width {
		if width <= 0 {
			return ""
		}
		return value[:min(len(value), width)]
	}
	return value[:width-3] + "..."
}
