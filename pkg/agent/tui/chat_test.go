package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

func TestLayoutRowsUsesIntrinsicHeightAndFillsRemainder(t *testing.T) {
	areas := layoutRows(80, 12,
		intrinsic("header\nmeta"),
		fill(),
		intrinsic("status"),
		intrinsic("composer\nline two\nline three"),
	)

	want := []int{2, 6, 1, 3}
	for i, height := range want {
		if areas[i].height != height {
			t.Fatalf("area %d height=%d want=%d", i, areas[i].height, height)
		}
	}
}

func TestComposerGrowsAndTranscriptYieldsSpace(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	initial := m.viewport.Height()

	m.composer.SetValue("one\ntwo\nthree\nfour")
	m.reflow(false)

	if m.composer.Height() <= 1 {
		t.Fatalf("composer height=%d want dynamic growth", m.composer.Height())
	}
	if m.viewport.Height() >= initial {
		t.Fatalf("viewport height=%d want less than initial %d", m.viewport.Height(), initial)
	}
	if got := sumAreaHeights(m.areas); got != 24 {
		t.Fatalf("allocated height=%d want=24", got)
	}
}

func TestReflowOnlyRendersTranscriptWhenWidthChanges(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "system", text: "initial"}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.blocks = append(m.blocks, block{role: "system", text: "pending refresh"})
	m.reflow(false)
	if content := ansi.Strip(m.viewport.GetContent()); strings.Contains(content, "pending refresh") {
		t.Fatalf("same-width reflow unexpectedly rebuilt transcript: %q", content)
	}

	m.width = 79
	m.reflow(false)
	if content := ansi.Strip(m.viewport.GetContent()); !strings.Contains(content, "pending refresh") {
		t.Fatalf("width-changing reflow did not rebuild transcript: %q", content)
	}
}

func TestComposerGrowthKeepsTranscriptAtBottom(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 20 {
		m.blocks = append(m.blocks, block{role: "system", text: fmt.Sprintf("line %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	if !m.viewport.AtBottom() {
		t.Fatal("transcript did not start at bottom")
	}

	m.composer.SetValue("one\ntwo\nthree\nfour")
	m.reflow(false)
	if !m.viewport.AtBottom() {
		t.Fatal("composer growth moved transcript away from bottom")
	}
}

func TestChecklistDeltaIsNotRenderedAsTranscriptText(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.appendDelta(contracts.DeltaTaskChecklist, `{"items":[]}`)

	if len(m.blocks) != 0 {
		t.Fatalf("checklist delta created %d transcript blocks", len(m.blocks))
	}
}

func TestEmptyCommandMenuDoesNotReserveARow(t *testing.T) {
	areas := layoutRows(80, 12,
		intrinsic("header"), fill(), intrinsic("status"), intrinsic(""), intrinsic("composer"),
	)
	if areas[3].height != 0 {
		t.Fatalf("empty command menu height=%d want 0", areas[3].height)
	}
	if areas[1].height != 9 {
		t.Fatalf("viewport height=%d want 9", areas[1].height)
	}
}

func TestEmptyCommandMenuDoesNotPushComposerPastTerminal(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})

	if height := lipgloss.Height(m.View().Content); height > 18 {
		t.Fatalf("chat view height=%d exceeds terminal height 18", height)
	}
}

func TestWorkingStatusIsProminentAndOmitsTurns(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width = 120
	sessionID := uuid.New()
	m.session = store.SessionMeta{ID: sessionID, Provider: "infai", Model: "gemma4-e2b-it"}
	m.thinking = contracts.ThinkingLow
	m.used, m.contextWindow = 6, 100
	normalStatus := ansi.Strip(m.statusView())
	if strings.Contains(normalStatus, "turn") {
		t.Fatalf("status contains turn count: %q", normalStatus)
	}
	for _, want := range []string{"gemma4-e2b-it (infai)", "thinking low", "[█░░░░░░░░░] 6%", sessionID.String()} {
		if !strings.Contains(normalStatus, want) {
			t.Fatalf("status lacks %q: %q", want, normalStatus)
		}
	}
	m.working = true
	m.workBegan = time.Now()
	workingStatus := ansi.Strip(m.statusView())
	if !strings.Contains(workingStatus, "working") {
		t.Fatalf("working status lacks activity label: %q", workingStatus)
	}
	if m.styles.statusBusy.GetForeground() != everforest.Yellow {
		t.Fatalf("working status foreground=%v want yellow", m.styles.statusBusy.GetForeground())
	}
}

func TestTranscriptPreservesUnicodeAndMarkdown(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "assistant", text: "## 概要\n\nUse `界面` with cafe\u0301."}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 16})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"概要", "界面", "cafe\u0301"} {
		if !strings.Contains(content, want) {
			t.Fatalf("view does not contain %q", want)
		}
	}
}

func TestTranscriptRendersThinkingMarkdown(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "thinking", text: "## Plan\n\nUse **careful reasoning**.\n\n- inspect\n- verify"}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 16})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Plan", "careful reasoning", "inspect", "verify"} {
		if !strings.Contains(content, want) {
			t.Fatalf("thinking view does not contain %q: %q", want, content)
		}
	}
	for _, rawMarkdown := range []string{"**careful reasoning**"} {
		if strings.Contains(content, rawMarkdown) {
			t.Fatalf("thinking view contains unrendered Markdown %q: %q", rawMarkdown, content)
		}
	}
}

func TestTranscriptUsesCompactRoleMarkers(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{
		{role: "user", text: "question"},
		{role: "thinking", text: "reasoning"},
		{role: "skill", text: "green-software"},
		{role: "tool", toolKind: "call", toolName: "search", text: `search {"path":"."}`},
		{role: "tool", toolKind: "result", toolStatus: "success", toolName: "search", text: "search success"},
		{role: "assistant", text: "answer"},
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"● question", "◌ reasoning", "✦ green-software", `▲ search {"path":"."}`, "▼ search success", "● answer"} {
		if !strings.Contains(content, want) {
			t.Fatalf("chat transcript does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"YOU", "THINKING", "Skill loaded:", "tool call:", "tool result:"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("chat transcript still contains %q", unwanted)
		}
	}
}

func TestToolCallPreviewFormatsKnownTools(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		want      []string
	}{
		{name: "read", arguments: `{"path":"pkg/agent/tui/chat.go","offset":10,"limit":25}`, want: []string{"pkg/agent/tui/chat.go", "lines 10-34"}},
		{name: "read", arguments: `{"path":"go.mod","metadata":true}`, want: []string{"go.mod", "metadata"}},
		{name: "bash", arguments: `{"command":"grep -E 'MemTotal' /proc/meminfo; lscpu","workdir":"scripts"}`, want: []string{"cwd  scripts", "$ grep -E 'MemTotal' /proc/meminfo; lscpu"}},
		{name: "write", arguments: `{"path":"notes.txt","content":"first\nsecond"}`, want: []string{"→ notes.txt", "2 lines, 12 bytes", "1  first", "2  second"}},
		{name: "edit", arguments: `{"path":"main.go","old_string":"old\nsame","new_string":"new\nsame","replace_all":true}`, want: []string{"diff --git a/main.go b/main.go", "--- a/main.go", "+++ b/main.go", "@@ -1,2 +1,2 @@", "\n-old\n+new\n same", "(every match)"}},
	}
	for _, tt := range tests {
		body := ansi.Strip(toolCallPreview(tt.name, tt.arguments))
		for _, want := range tt.want {
			if !strings.Contains(body, want) {
				t.Fatalf("tool preview for %q lacks %q: %q", tt.name, want, body)
			}
		}
	}
}

func TestParseUnifiedRowsTracksLineNumbers(t *testing.T) {
	rows := parseUnifiedRows("--- a/x\n+++ b/x\n@@ -10,3 +20,3 @@\n context\n-removed\n+added\n tail")
	want := []diffRow{
		{marker: '@'},
		{oldNum: 10, newNum: 20, marker: ' '},
		{oldNum: 11, marker: '-'},
		{newNum: 21, marker: '+'},
		{oldNum: 12, newNum: 22, marker: ' '},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d: %#v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i].oldNum != want[i].oldNum || rows[i].newNum != want[i].newNum || rows[i].marker != want[i].marker {
			t.Fatalf("row %d = %#v want %#v", i, rows[i], want[i])
		}
	}
}

func TestWordDiffSegmentsEmphasizeChanges(t *testing.T) {
	oldSegments, newSegments := wordDiffSegments("return old_value", "return new_value")
	emphasized := func(segments []diffSegment) string {
		var b strings.Builder
		for _, segment := range segments {
			if segment.emph {
				b.WriteString(segment.text)
			}
		}
		return b.String()
	}
	if got := emphasized(oldSegments); got != "old" {
		t.Fatalf("old emphasis=%q want %q", got, "old")
	}
	if got := emphasized(newSegments); got != "new" {
		t.Fatalf("new emphasis=%q want %q", got, "new")
	}
}

func TestRenderEditDiffBlockShowsGuttersAndEmphasis(t *testing.T) {
	styles := newHarnessStyles()
	args := `{"path":"main.go","old_string":"return old","new_string":"return new"}`
	rendered := ansi.Strip(renderEditDiffBlock("▲", styles.system, styles, args, 70))
	for _, want := range []string{"edit  main.go", "@@ -1 +1 @@", "1   - return old", "  1 + return new"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("edit diff lacks %q:\n%s", want, rendered)
		}
	}
}

func TestToolNameUsesMarkerEmphasis(t *testing.T) {
	styles := newHarnessStyles()
	rendered := renderToolMarker("▲", styles.system, styles.tool, "search", `search {"path":"."}`, 60)
	if !strings.Contains(rendered, styles.system.Bold(true).Render("search")) {
		t.Fatal("tool name does not use the emphasized marker style")
	}
}

func TestMarkdownMathIsReadable(t *testing.T) {
	input := `- $\text{OperationalCarbon}$ is $\text{EnergyKWh} \times \text{median}(samples)$.`
	got := normalizeMarkdownMath(input)
	if strings.Contains(got, `\text`) || strings.Contains(got, "$Operational") {
		t.Fatalf("math commands remain visible: %q", got)
	}
	for _, want := range []string{"OperationalCarbon", "EnergyKWh", "×", "median"} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized markdown does not contain %q: %q", want, got)
		}
	}
}

func TestMarkdownMathHandlesDisplayBlocksWithoutChangingCurrency(t *testing.T) {
	input := "Cost is $5 and $10.\n\n$$\n\\frac{energy}{work} \\geq 1\n$$\n\n`$\\text{literal}$`"
	got := normalizeMarkdownMath(input)
	if !strings.Contains(got, "$5 and $10") {
		t.Fatalf("currency was changed: %q", got)
	}
	if !strings.Contains(got, "(energy)/(work) ≥ 1") {
		t.Fatalf("display math was not normalized: %q", got)
	}
	if !strings.Contains(got, "`$\\text{literal}$`") {
		t.Fatalf("inline code was changed: %q", got)
	}
}

func TestPasteReachesComposer(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	_, _ = m.Update(tea.PasteMsg{Content: "first line\nsecond line"})

	if got := m.composer.Value(); got != "first line\nsecond line" {
		t.Fatalf("composer value=%q", got)
	}
}

func TestSlashOpensComposerCompletionInsteadOfModal(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))

	if !m.commandMenu {
		t.Fatal("command completion is not open")
	}
	if m.modal != nil {
		t.Fatal("slash completion opened a modal")
	}
	if content := m.View().Content; !strings.Contains(content, "/timeline") {
		t.Fatal("command completion is not rendered beside the composer")
	}
}

func TestMouseWheelScrollsTranscript(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 40 {
		m.blocks = append(m.blocks, block{role: "system", text: fmt.Sprintf("event %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	before := m.viewport.YOffset()
	_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 2, Button: tea.MouseWheelUp}))

	if after := m.viewport.YOffset(); after >= before {
		t.Fatalf("viewport offset=%d want less than %d after wheel up", after, before)
	}
}

func TestSessionScreenShowsActiveAndInactiveStatus(t *testing.T) {
	active := uuid.New()
	inactive := uuid.New()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.session.ID = active
	m.showSessions([]contracts.SessionSummary{
		{ID: active, Model: "active-model", Cwd: "/active"},
		{ID: inactive, Model: "saved-model", Cwd: "/saved"},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	content := m.View().Content
	for _, want := range []string{"[ACTIVE]", "[INACTIVE]", "active-model", "saved-model"} {
		if !strings.Contains(content, want) {
			t.Fatalf("session screen does not contain %q", want)
		}
	}
}

func TestSessionWorkspaceShowsBrandAndSections(t *testing.T) {
	active := uuid.New()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.session.ID = active
	m.showSessions([]contracts.SessionSummary{
		{ID: active, Model: "active-model", Cwd: "/active"},
		{ID: uuid.New(), Model: "saved-model", Cwd: "/saved"},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"INFAI HARNESS", "SESSION WORKSPACE", "NEW SESSION", "ACTIVE SESSIONS", "ALL SESSIONS", "active-model", "saved-model"} {
		if !strings.Contains(content, want) {
			t.Fatalf("session workspace does not contain %q", want)
		}
	}
	if width := lipgloss.Width(m.View().Content); width > 120 {
		t.Fatalf("session workspace width=%d exceeds terminal", width)
	}
	if height := lipgloss.Height(m.View().Content); height > 30 {
		t.Fatalf("session workspace height=%d exceeds terminal", height)
	}
}

func TestWorkingTurnDoesNotQueueInputOrOpenSessions(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.working = true
	turnCtx, cancel := context.WithCancel(context.Background())
	m.turnCancel = cancel
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	_, _ = m.Update(tea.PasteMsg{Content: "queued"})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	escape := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	_, _ = m.Update(escape)
	if turnCtx.Err() != nil {
		t.Fatal("first escape canceled the working turn")
	}
	_, _ = m.Update(escape)
	if !errors.Is(turnCtx.Err(), context.Canceled) {
		t.Fatal("second escape did not cancel the working turn")
	}

	if m.composer.Value() != "" {
		t.Fatalf("working turn queued composer input %q", m.composer.Value())
	}
	if m.modal != nil {
		t.Fatal("working turn opened the session workspace")
	}
}

func TestNormalizeMarkdownMath(t *testing.T) {
	input := `Operational carbon is $\text{EnergyKWh} \times \mathrm{Intensity}$ and $\frac{a}{b}$.`
	want := "Operational carbon is EnergyKWh × Intensity and (a)/(b)."
	if got := normalizeMarkdownMath(input); got != want {
		t.Fatalf("normalized math=%q want=%q", got, want)
	}
}

func TestModalMeasuresContentWithinTerminal(t *testing.T) {
	m := &modalModel{
		title: "Models",
		body:  "Choose one",
		options: []modalOption{
			{label: "small"},
			{label: "a considerably longer model name"},
		},
	}
	rendered := renderModal(m, 52, 12, newHarnessStyles())
	if width := lipgloss.Width(rendered); width > 52 {
		t.Fatalf("modal width=%d exceeds terminal width", width)
	}
	if height := lipgloss.Height(rendered); height > 12 {
		t.Fatalf("modal height=%d exceeds terminal height", height)
	}
}

func TestLongModalAndShortLayoutStayWithinTerminal(t *testing.T) {
	options := make([]modalOption, 30)
	for i := range options {
		options[i] = modalOption{label: strings.Repeat("long option ", 8)}
	}
	m := &modalModel{
		title:    "Approval",
		body:     strings.Repeat("long approval details ", 40),
		options:  options,
		selected: len(options) - 1,
	}
	rendered := renderModal(m, 40, 10, newHarnessStyles())
	if width := lipgloss.Width(rendered); width > 40 {
		t.Fatalf("modal width=%d exceeds terminal width", width)
	}
	if height := lipgloss.Height(rendered); height > 10 {
		t.Fatalf("modal height=%d exceeds terminal height", height)
	}

	areas := layoutRows(20, 2, intrinsic("header"), fill(), intrinsic("status"), intrinsic("composer"))
	if got := sumAreaHeights(areas); got != 2 {
		t.Fatalf("short layout height=%d want=2", got)
	}
}

func TestApprovalOverlayKeepsTranscriptVisible(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "system", text: "transcript remains visible"}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	m.showApproval(&Approval{Message: "Run this command?"})

	content := m.View().Content
	for _, want := range []string{"transcript remains visible", "APPROVAL REQUIRED", "Run this command?", "[A]llow", "[D]eny"} {
		if !strings.Contains(content, want) {
			t.Fatalf("approval view does not contain %q", want)
		}
	}
}

func TestApprovalModalPinsActionsWhileBodyScrolls(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("review line %02d", i+1)
	}
	modal := &modalModel{
		kind: modalApproval, title: "Approval required", body: strings.Join(lines, "\n"),
		options: []modalOption{
			{label: "Allow", shortcut: 'a'},
			{label: "Deny", shortcut: 'd'},
		},
	}
	rendered := ansi.Strip(renderModal(modal, 70, 12, newHarnessStyles()))
	for _, want := range []string{"review line 01", "[A]llow", "[D]eny", "review lines 1-"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("approval modal lacks %q: %q", want, rendered)
		}
	}

	modal.bodyOffset = 8
	rendered = ansi.Strip(renderModal(modal, 70, 12, newHarnessStyles()))
	if strings.Contains(rendered, "review line 01") || !strings.Contains(rendered, "review line 09") {
		t.Fatalf("approval body did not scroll: %q", rendered)
	}
}

func TestApprovalReviewScrollControls(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.width, m.height = 70, 12
	m.modal = &modalModel{kind: modalApproval, title: "Approval", body: strings.Repeat("review line\n", 30)}

	_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown}))
	if m.modal.bodyOffset != 3 {
		t.Fatalf("mouse wheel body offset=%d want 3", m.modal.bodyOffset)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.modal.bodyOffset != 11 {
		t.Fatalf("page down body offset=%d want 11", m.modal.bodyOffset)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.modal.bodyOffset != 3 {
		t.Fatalf("page up body offset=%d want 3", m.modal.bodyOffset)
	}
	for range 100 {
		_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown}))
	}
	maxOffset := approvalMaxBodyOffset(m.modal, m.width, m.height, m.styles)
	if m.modal.bodyOffset != maxOffset {
		t.Fatalf("overscroll body offset=%d want bounded maximum %d", m.modal.bodyOffset, maxOffset)
	}
	_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelUp}))
	if m.modal.bodyOffset != max(maxOffset-3, 0) {
		t.Fatalf("reverse scroll body offset=%d did not move immediately from maximum %d", m.modal.bodyOffset, maxOffset)
	}
}

func TestApprovalModalRendersEditDiff(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.EditTool),
			Arguments: `{"path":"main.go","old_string":"return old","new_string":"return new"}`,
		},
	}})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"EDIT FILE", "TARGET  main.go", "@@ -1 +1 @@", "1   - return old", "  1 + return new", "[A]llow", "[D]eny"} {
		if !strings.Contains(content, want) {
			t.Fatalf("edit approval modal lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "BEFORE") || strings.Contains(content, "AFTER") {
		t.Fatalf("edit approval modal still shows BEFORE/AFTER:\n%s", content)
	}
}

func TestEditDiffWrapsAndKeepsActionsPinned(t *testing.T) {
	long := strings.Repeat("wrapping content ", 12)
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.EditTool),
			Arguments: `{"path":"x.md","old_string":"` + long + `","new_string":"` + long + ` changed"}`,
		},
	}})

	view := m.View().Content
	if height := lipgloss.Height(view); height > 20 {
		t.Fatalf("modal view height=%d exceeds terminal height 20", height)
	}
	content := ansi.Strip(view)
	for _, want := range []string{"[A]llow", "[D]eny"} {
		if !strings.Contains(content, want) {
			t.Fatalf("wrapped edit approval lost pinned action %q:\n%s", want, content)
		}
	}
}

func TestApprovalModalRendersWriteAdditions(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.WriteTool),
			Arguments: `{"path":"notes.txt","content":"first line\nsecond line"}`,
		},
	}})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"WRITE FILE", "TARGET  notes.txt", "1 + first line", "2 + second line"} {
		if !strings.Contains(content, want) {
			t.Fatalf("write approval modal lacks %q:\n%s", want, content)
		}
	}
}

func TestApprovalToolCallFormatting(t *testing.T) {
	tests := []struct {
		name      string
		tool      contracts.ToolType
		arguments string
		want      []string
	}{
		{
			name: "read", tool: contracts.ReadTool,
			arguments: `{"path":"pkg/agent/tui/chat.go","offset":10,"limit":25}`,
			want:      []string{"Read file", "SOURCE  pkg/agent/tui/chat.go", "lines 10-34"},
		},
		{
			name: "bash", tool: contracts.BashTool,
			arguments: `{"command":"printf 'hello\\nworld'\nprintf done","workdir":"scripts","timeout":30}`,
			want:      []string{"Bash tool call", "WORKING DIRECTORY  scripts", "TIMEOUT            30 seconds", "printf 'hello\\nworld'\nprintf done"},
		},
		{
			name: "write", tool: contracts.WriteTool,
			arguments: `{"path":"notes.txt","content":"first line\nsecond line"}`,
			want:      []string{"Write file", "TARGET  notes.txt", "2 lines", "EFFECT  Replace complete file contents"},
		},
		{
			name: "edit", tool: contracts.EditTool,
			arguments: `{"path":"main.go","old_string":"old\ntext","new_string":"new\ntext","replace_all":true}`,
			want:      []string{"Edit file", "TARGET  main.go", "Replace every exact match"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, body, script := formatApprovalToolCall(contracts.ToolCall{Function: contracts.Function{Name: string(tt.tool), Arguments: tt.arguments}})
			formatted := title + "\n" + body + "\n" + script
			for _, want := range tt.want {
				if !strings.Contains(formatted, want) {
					t.Fatalf("formatted approval lacks %q: %q", want, formatted)
				}
			}
		})
	}
}

func TestApprovalModalRendersBashAsCode(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name:      string(contracts.BashTool),
		Arguments: `{"command":"if test -f go.mod; then\n  go test ./...\nfi","workdir":"."}`,
	}}})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"BASH TOOL CALL", "SCRIPT", "if test -f go.mod; then", "go test ./...", "fi"} {
		if !strings.Contains(content, want) {
			t.Fatalf("bash approval modal lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "```bash") {
		t.Fatalf("bash approval modal exposes Markdown fence:\n%s", content)
	}

	highlighted := renderApprovalScript("nvidia-smi", 40, newHarnessStyles())
	if !strings.Contains(highlighted, "\x1b[48;2;46;56;60m") {
		t.Fatalf("bash approval script does not use modal surface background: %q", highlighted)
	}
	if strings.Contains(highlighted, "\x1b[48;2;39;46;51m") {
		t.Fatalf("bash approval script uses app background: %q", highlighted)
	}
	if got := strings.TrimSpace(ansi.Strip(highlighted)); got != "nvidia-smi" {
		t.Fatalf("bash approval script=%q", got)
	}
}

func TestCommandMenuKeepsSelectedCommandVisible(t *testing.T) {
	rendered := renderCommandMenu(harnessCommands, len(harnessCommands)-1, 50, 2, newHarnessStyles())
	if !strings.Contains(rendered, "/quit") {
		t.Fatal("selected command was clipped from a short completion menu")
	}
	if lipgloss.Height(rendered) > 2 {
		t.Fatalf("command menu height=%d want at most 2", lipgloss.Height(rendered))
	}
}

func sumAreaHeights(areas []rowArea) int {
	total := 0
	for _, area := range areas {
		total += area.height
	}
	return total
}

func TestTimelineTreeOrderPlacesBranchBelowParent(t *testing.T) {
	firstAssistant := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	mainUser := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	branchUser := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	branchReply := uuid.MustParse("00000000-0000-0000-0000-000000000005")

	selected := firstAssistant
	rows := timelineTreeRows([]TimelineEvent{
		{ID: branchReply, ParentID: branchUser},
		{ID: mainUser, ParentID: firstAssistant},
		{ID: branchUser, ParentID: firstAssistant, BranchFrom: &selected},
		{ID: firstAssistant, ParentID: uuid.Nil},
	})

	got := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.event.ID)
	}
	want := []uuid.UUID{firstAssistant, branchUser, branchReply, mainUser}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tree order=%v want=%v", got, want)
		}
	}
}

func TestTimelineTreeRowsShowForkWithoutMessageStaircase(t *testing.T) {
	root := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	main := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	branch := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	branchReply := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	branchFrom := root

	rows := timelineTreeRows([]TimelineEvent{
		{ID: root},
		{ID: main, ParentID: root},
		{ID: branch, ParentID: root, BranchFrom: &branchFrom},
		{ID: branchReply, ParentID: branch},
	})
	want := []string{"● ", "├─ ", "│  │ ", "└─ "}
	for i := range want {
		if rows[i].prefix != want[i] {
			t.Fatalf("row %d prefix=%q want=%q", i, rows[i].prefix, want[i])
		}
	}
	if rows[1].fork != "branch" || rows[2].fork != "" || rows[3].fork != "original" {
		t.Fatalf("fork labels=%q, %q, %q", rows[1].fork, rows[2].fork, rows[3].fork)
	}
	wantSubprefix := []string{"│ ", "│  ", "│  │ ", "   "}
	for i := range wantSubprefix {
		if rows[i].subprefix != wantSubprefix[i] {
			t.Fatalf("row %d subprefix=%q want=%q", i, rows[i].subprefix, wantSubprefix[i])
		}
	}
}

func TestTimelinePopupKeepsTranscriptAndHidesEventIDs(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "system", text: "transcript remains visible"}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	eventID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	contentText := "explain this branch"
	m.showTimeline(&TimelineView{Head: eventID, Events: []TimelineEvent{{
		ID: eventID,
		Record: &store.Record{Kind: store.KindMessage, Message: &contracts.ChatMessage{
			Role: "user", Content: &contentText,
		}},
	}}})

	content := m.View().Content
	for _, want := range []string{"transcript remains visible", "BRANCH TIMELINE", "* marks the current event", "user:", "explain this branch"} {
		if !strings.Contains(content, want) {
			t.Fatalf("timeline popup does not contain %q", want)
		}
	}
	if strings.Contains(content, eventID.String()) || strings.Contains(content, shortID(eventID)) {
		t.Fatal("timeline popup exposes an event ID")
	}
	if !m.modal.options[0].current {
		t.Fatal("timeline head is not marked as the current event")
	}
}

func TestTimelineBranchUsesColoredUnicodeGlyph(t *testing.T) {
	rendered := renderTimelineOption(modalOption{
		label: "alternate prompt", role: "user", tree: "├─ ", fork: "branch",
	}, false, 50, newHarnessStyles())
	if !strings.Contains(rendered, "⎇") {
		t.Fatal("timeline branch does not contain the branch glyph")
	}
	if strings.Contains(rendered, "branch") {
		t.Fatal("timeline branch still contains the textual branch label")
	}
}

func TestTimelineOriginalHasNoTextLabel(t *testing.T) {
	rendered := renderTimelineOption(modalOption{
		label: "existing prompt", role: "user", tree: "└─ ", fork: "original",
	}, false, 50, newHarnessStyles())
	if strings.Contains(rendered, "original") {
		t.Fatal("timeline original path still contains a text label")
	}
}

func TestTimelineRoleColors(t *testing.T) {
	tests := map[string]string{
		"user": "4", "thinking": "8", "assistant": "10",
		"tool_call": "13", "tool_result": "13", "skill": "6",
	}
	for role, want := range tests {
		got := timelineRoleStyle(lipgloss.NewStyle(), role).GetForeground()
		if got != lipgloss.Color(want) {
			t.Errorf("role %s color=%v want=%v", role, got, lipgloss.Color(want))
		}
	}
}

func TestTimelineEventDisplayUsesSupportedRoles(t *testing.T) {
	call := contracts.ToolCall{Function: contracts.Function{Name: string(contracts.ReadSkillTool), Arguments: `{"name":"code-review"}`}}
	displays := timelineEventDisplays(TimelineEvent{Record: &store.Record{
		Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}},
	}})
	if len(displays) != 1 || displays[0].role != "skill" || displays[0].text != "code-review" {
		t.Fatalf("timeline displays=%#v want one code-review skill", displays)
	}
}

func TestTimelineEventDisplaysThinkingAndAnswer(t *testing.T) {
	answer := "Final answer"
	event := TimelineEvent{ID: uuid.New(), Record: &store.Record{
		Kind: store.KindMessage, Message: &contracts.ChatMessage{
			Role: "assistant", Content: &answer, ReasoningContent: "Reasoning process",
		},
	}}
	displays := timelineEventDisplays(event)
	if len(displays) != 2 || displays[0].role != "thinking" || displays[1].role != "assistant" {
		t.Fatalf("timeline displays=%#v want thinking then assistant", displays)
	}

	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.showTimeline(&TimelineView{Head: event.ID, Events: []TimelineEvent{event}})
	content := m.View().Content
	for _, want := range []string{"thinking:", "Reasoning process", "assistant:", "Final answer"} {
		if !strings.Contains(content, want) {
			t.Fatalf("timeline popup does not contain %q", want)
		}
	}
	if len(m.modal.options) != 2 || m.modal.options[0].event.ID != m.modal.options[1].event.ID {
		t.Fatal("thinking and assistant rows do not select the same event")
	}
	if m.modal.options[0].current || !m.modal.options[1].current || m.modal.selected != 1 {
		t.Fatal("compound event HEAD is not anchored to its final display row")
	}
	if m.modal.options[1].tree != "│ " {
		t.Fatalf("assistant subrow tree=%q want connected vertical guide", m.modal.options[1].tree)
	}
}

func TestBlocksFromRecordsShowsToolCallsAndResults(t *testing.T) {
	call := contracts.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: contracts.Function{
			Name:      "read",
			Arguments: `{"path":"README.md"}`,
		},
	}
	records := []store.Record{
		{Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}}},
		{Kind: store.KindToolResult, ToolResult: &store.ToolResultRecord{CallID: call.ID, Status: "success", Output: `{"content":"hello"}`}},
	}

	blocks := blocksFromRecords(records)
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d want 2: %#v", len(blocks), blocks)
	}
	if blocks[0].role != "tool" || blocks[0].text != "README.md" {
		t.Fatalf("tool call block=%#v", blocks[0])
	}
	if blocks[1].role != "tool" || blocks[1].text != "success · 1 line, 19 bytes" {
		t.Fatalf("tool result block=%#v", blocks[1])
	}
	if blocks[1].toolName != "read" {
		t.Fatalf("tool result name=%q want read", blocks[1].toolName)
	}
}

func TestReadToolResultSummaryPreservesErrors(t *testing.T) {
	if got := transcriptToolResultDisplay("read", "success", "one\ntwo\n", ""); got != "success · 2 lines, 8 bytes" {
		t.Fatalf("successful read summary=%q", got)
	}
	if got := transcriptToolResultDisplay("read", "error", "", "permission denied"); got != "error: permission denied" {
		t.Fatalf("read error=%q want full error", got)
	}
	bashOutput := `{"exit_code":7,"output":"full output\n","truncated":true}`
	if got := transcriptToolResultDisplay("bash", "success", bashOutput, ""); got != "success · exit 7 · output truncated\nfull output\n" {
		t.Fatalf("bash result=%q", got)
	}
	if got := transcriptToolResultDisplay("bash", "success", "legacy output", ""); got != "success\nlegacy output" {
		t.Fatalf("legacy bash result=%q", got)
	}
}

func TestLiveAndResumedBashResultsMatch(t *testing.T) {
	payload := `{"exit_code":7,"output":"full output\n","truncated":true}`
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.appendDelta(contracts.DeltaToolResult, "bash [success]\n"+payload)
	if len(m.blocks) != 1 {
		t.Fatalf("live blocks=%d", len(m.blocks))
	}

	call := contracts.ToolCall{ID: "call-1", Type: "function", Function: contracts.Function{Name: string(contracts.BashTool)}}
	toolMessage := contracts.NewToolMessage(call.ID, payload, contracts.ToolExecutionSuccess)
	resumed := blocksFromRecords([]store.Record{
		{Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}}},
		{Kind: store.KindMessage, Message: &toolMessage},
	})
	if len(resumed) != 2 {
		t.Fatalf("resumed blocks=%d", len(resumed))
	}
	if m.blocks[0].text != resumed[1].text {
		t.Fatalf("live result %q != resumed result %q", m.blocks[0].text, resumed[1].text)
	}
	if !strings.Contains(m.blocks[0].text, "full output") {
		t.Fatalf("live result omits bash output: %q", m.blocks[0].text)
	}
}

func TestDiffRowsCarrySyntaxColors(t *testing.T) {
	rows := editDiffRows("main.go", "return oldValue\n", "return newValue\n")
	if len(rows) == 0 {
		t.Fatal("edit diff produced no rows")
	}
	colored := false
	for _, row := range rows {
		for _, segment := range row.segments {
			if segment.fg != nil && !segment.emph {
				colored = true
			}
		}
	}
	if !colored {
		t.Fatal("edit diff rows carry no syntax colors")
	}
}

func TestCycleThinking(t *testing.T) {
	m := &chatModel{availableThinking: []contracts.InfaiThinkingLevel{contracts.ThinkingOff, contracts.ThinkingLow, contracts.ThinkingHigh}}

	for _, want := range []contracts.InfaiThinkingLevel{contracts.ThinkingOff, contracts.ThinkingLow, contracts.ThinkingHigh, ""} {
		m.cycleThinking()
		if got := m.thinking; got != want {
			t.Fatalf("cycleThinking() = %q, want %q", got, want)
		}
	}
}
