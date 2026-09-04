package tui

import (
	"context"
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
	m.width = 80
	m.session = store.SessionMeta{ID: uuid.New(), Model: "gemma4-e2b-it", TurnCount: 7}
	normalStatus := ansi.Strip(m.statusView())
	if strings.Contains(normalStatus, "turn") {
		t.Fatalf("status contains turn count: %q", normalStatus)
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
	if content := m.View().Content; !strings.Contains(content, "/branch-timeline") {
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
	m.showSessions([]store.SessionMeta{
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
	for _, want := range []string{"transcript remains visible", "APPROVAL REQUIRED", "Run this command?"} {
		if !strings.Contains(content, want) {
			t.Fatalf("approval view does not contain %q", want)
		}
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
	if blocks[0].role != "tool" || blocks[0].text != `read {"path":"README.md"}` {
		t.Fatalf("tool call block=%#v", blocks[0])
	}
	if blocks[1].role != "tool" || blocks[1].text != "success\n{\"content\":\"hello\"}" {
		t.Fatalf("tool result block=%#v", blocks[1])
	}
	if blocks[1].toolName != "read" {
		t.Fatalf("tool result name=%q want read", blocks[1].toolName)
	}
}
