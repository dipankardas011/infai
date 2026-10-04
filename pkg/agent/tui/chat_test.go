package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
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
	m.reflow()

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
	m.reflow()
	if content := ansi.Strip(m.viewport.GetContent()); strings.Contains(content, "pending refresh") {
		t.Fatalf("same-width reflow unexpectedly rebuilt transcript: %q", content)
	}

	m.width = 79
	m.reflow()
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
	m.reflow()
	if !m.viewport.AtBottom() {
		t.Fatal("composer growth moved transcript away from bottom")
	}
}

// renderedBlocks counts the blocks holding a cached render: the transcript
// window is what decides that count.
func renderedBlocks(m *chatModel) int {
	count := 0
	for i := range m.blocks {
		if m.blocks[i].renderedValid {
			count++
		}
	}
	return count
}

func TestTranscriptRendersOnlyTheWindowAroundTheAnchor(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	rendered := renderedBlocks(m)
	if rendered == len(m.blocks) {
		t.Fatalf("all %d blocks were rendered; the window should hold only what the screen reaches", rendered)
	}
	if rendered == 0 {
		t.Fatal("the window rendered nothing")
	}
	if m.blocks[0].renderedValid {
		t.Fatal("the first block was rendered although the view is pinned to the newest output")
	}
	if !m.blocks[len(m.blocks)-1].renderedValid {
		t.Fatal("the newest block is outside the window")
	}

	// Scrolling up renders the blocks the view reaches, and only those.
	before := rendered
	m.scrollTranscript(-m.transcriptHeight())
	after := renderedBlocks(m)
	if after <= before {
		t.Fatalf("scrolling up rendered %d blocks, want more than the %d already rendered", after, before)
	}
	if after == len(m.blocks) {
		t.Fatalf("scrolling one screen rendered all %d blocks", after)
	}
}

func TestResizeRendersOnlyTheWindow(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// A resize invalidates every block rendered at the old width, but only the
	// ones in the window are rendered again.
	_, _ = m.Update(tea.WindowSizeMsg{Width: 81, Height: 24})
	if rendered := renderedBlocks(m); rendered == len(m.blocks) {
		t.Fatalf("resize rendered all %d blocks", rendered)
	}
	for i := range m.blocks {
		if m.blocks[i].renderedValid && m.blocks[i].renderedWidth != m.transcriptWidth() {
			t.Fatalf("block %d kept a rendering from the old width", i)
		}
	}
}

func TestScrollingUpAndDownKeepsThePosition(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.atBottom() {
		t.Fatal("a fresh transcript is not pinned to the newest output")
	}

	m.scrollTranscript(-m.transcriptHeight())
	if m.atBottom() {
		t.Fatal("scrolling up still reports the newest output")
	}
	top := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(top, "Block ") {
		t.Fatalf("scrolled transcript is empty:\n%s", top)
	}

	m.scrollTranscript(m.transcriptHeight())
	if !m.atBottom() {
		t.Fatal("scrolling back down did not reach the newest output")
	}
}

func TestResizeKeepsTheReaderOnTheSameBlock(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.scrollTranscript(-3 * m.transcriptHeight())
	anchored := m.anchor
	if m.atBottom() {
		t.Fatal("scrolling up left the view pinned to the newest output")
	}

	// A resize reflows the blocks, but the block the reader was on stays at the
	// top and the view does not jump to the newest output.
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if m.anchor.block != anchored.block {
		t.Fatalf("resize moved the top block from %d to %d", anchored.block, m.anchor.block)
	}
	if m.atBottom() {
		t.Fatal("resize jumped the view to the newest output")
	}
	content := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(content, fmt.Sprintf("Block %d", m.anchor.block)) {
		t.Fatalf("the anchored block is not rendered after the resize:\n%s", content)
	}
}

func TestResizeKeepsFollowingTheNewestOutput(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.atBottom() {
		t.Fatal("a fresh transcript is not pinned to the newest output")
	}

	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if !m.atBottom() {
		t.Fatal("a resize unpinned a view that was following the newest output")
	}
	content := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(content, "Block 199") {
		t.Fatalf("the newest block left the window on resize:\n%s", content)
	}
}

func TestFollowKeepsTheNewestOutputVisible(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 60 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.blocks = append(m.blocks, block{role: "assistant", text: "## Newest"})
	m.refreshTranscript(true)
	if !m.atBottom() {
		t.Fatal("following did not pin the view to the newest block")
	}
	if content := ansi.Strip(m.viewport.GetContent()); !strings.Contains(content, "Newest") {
		t.Fatalf("the newest block is not in the window:\n%s", content)
	}
}

func TestApprovalDetailScrollsWithTheTranscript(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	for i := range 40 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"ls","workdir":"/w"}`,
	}}})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	m.refreshTranscript(true)

	content := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(content, "Human In the Loop") {
		t.Fatalf("expanded decision detail is missing from the window:\n%s", content)
	}
	if !m.atBottom() {
		t.Fatal("the expanded detail should stay pinned with the transcript end")
	}
}

// A pending decision owns the keyboard, so the composer greys out, drops its
// cursor and says why — while keeping the draft that is waiting in it.
func TestPendingDecisionDimsTheComposerAndKeepsTheDraft(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m.composer.SetValue("a queued prompt")
	m.reflow()

	if got := m.composer.Styles().Focused.Text.GetForeground(); got != everforest.Text {
		t.Fatalf("composer text is %v while it takes prompts, want the bright text colour", got)
	}
	// The virtual cursor draws a reverse-video cell for the character it sits
	// on, which is how a live composer says where typing would land.
	if !strings.Contains(m.composer.View(), "\x1b[7;") {
		t.Fatal("a composer taking prompts draws no cursor cell")
	}

	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"ls"}`,
	}}})

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "a queued prompt") {
		t.Fatalf("the pending decision dropped the draft:\n%s", view)
	}
	if !strings.Contains(view, inputMark) {
		t.Fatalf("the waiting composer changed its mark instead of only greying:\n%s", view)
	}
	styles := m.composer.Styles().Focused
	if got := styles.Text.GetForeground(); got != everforest.Muted {
		t.Fatalf("composer text is %v while a decision waits, want the muted colour", got)
	}
	if got := styles.Prompt.GetForeground(); got != everforest.Muted {
		t.Fatalf("composer mark is %v while a decision waits, want the muted colour", got)
	}
	if strings.Contains(m.composer.View(), "\x1b[7;") {
		t.Fatal("a waiting composer still draws a cursor cell")
	}
	if m.styles.composerWaiting.Render("x") == m.styles.composer.Render("x") {
		t.Fatal("the waiting composer frame does not recede")
	}

	// Answering hands the composer back with the draft intact.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	if got := m.composer.Styles().Focused.Text.GetForeground(); got != everforest.Text {
		t.Fatalf("composer text stayed dim after the decision: %v", got)
	}
	if got := m.composer.Value(); got != "a queued prompt" {
		t.Fatalf("the draft did not survive the decision: %q", got)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, inputMark) {
		t.Fatalf("the composer did not take its prompt mark back:\n%s", view)
	}
}

// Capturing a reason is the one thing a pending decision asks the composer to
// do, so that mode keeps the composer live and marked as the reason field.
func TestReasonModeKeepsTheComposerActive(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m.composer.SetValue("a queued prompt")
	m.reflow()
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"ls"}`,
	}}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if got := m.composer.Styles().Focused.Text.GetForeground(); got != everforest.Text {
		t.Fatalf("the reason field is dimmed: %v", got)
	}
	if !strings.Contains(m.composer.View(), "\x1b[7;") {
		t.Fatal("the reason field draws no cursor cell")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "why▸") {
		t.Fatalf("the reason field is not marked:\n%s", view)
	}

	// Leaving reason mode puts the composer back to waiting, draft and all.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if got := m.composer.Styles().Focused.Text.GetForeground(); got != everforest.Muted {
		t.Fatalf("the composer did not go back to waiting: %v", got)
	}
	if got := m.composer.Value(); got != "a queued prompt" {
		t.Fatalf("leaving reason mode lost the draft: %q", got)
	}
}

func TestChecklistDeltaIsNotRenderedAsTranscriptText(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.appendDelta(contracts.EventToolTaskCheckList, `{"items":[]}`)

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
	m.session = store.SessionMeta{ID: uuid.New(), Provider: "infai", Model: "gemma4-e2b-it"}
	m.thinking = contracts.ThinkingLow
	m.used, m.contextWindow = 6, 100
	normalStatus := ansi.Strip(m.statusView())
	if strings.Contains(normalStatus, "turn") {
		t.Fatalf("status contains turn count: %q", normalStatus)
	}
	for _, want := range []string{"gemma4-e2b-it (infai)", "thinking low", "ctx ▎░░░░░ 6% 6/100"} {
		if !strings.Contains(normalStatus, want) {
			t.Fatalf("status lacks %q: %q", want, normalStatus)
		}
	}
	if strings.Contains(normalStatus, m.session.ID.String()) {
		t.Fatalf("status still shows the session id: %q", normalStatus)
	}
	m.applySessionStatus(contracts.SessionBusy)
	workingStatus := ansi.Strip(m.sessionRowView())
	if !strings.Contains(workingStatus, "busy") {
		t.Fatalf("session row lacks the session status: %q", workingStatus)
	}
	if !strings.Contains(workingStatus, spinnerFrame(m.workBegan)) || !strings.Contains(workingStatus, "0s") {
		t.Fatalf("session row lacks the spinner and timer: %q", workingStatus)
	}
	if m.styles.statusBusy.GetForeground() != everforest.Yellow {
		t.Fatalf("working status foreground=%v want yellow", m.styles.statusBusy.GetForeground())
	}
}

// The session row carries the three things that describe the session — its name,
// the kind of agent running it, and its status — and the bottom bar keeps the
// facts about the model and the context.
func TestSessionRowCarriesNameKindAndStatus(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width = 120
	m.session = store.SessionMeta{
		ID: uuid.New(), Name: "Deploy the cluster", Provider: "deepseek",
		Model: "deepseek-v4-flash", Cwd: "/ws/infai", AgentKind: contracts.SidecarLoopAgent,
	}
	m.status = contracts.SessionWaitingApproval

	row := ansi.Strip(m.sessionRowView())
	for _, want := range []string{"Deploy the cluster", "⧉", "waiting for approval"} {
		if !strings.Contains(row, want) {
			t.Fatalf("session row lacks %q: %q", want, row)
		}
	}
	// The marks stay flush right, as the status row always did.
	if !strings.HasSuffix(strings.TrimRight(row, " "), "waiting for approval") {
		t.Fatalf("the marks are not right-aligned: %q", row)
	}

	// A row with no room for the name keeps the marks and drops the name, and the
	// marks stay on the right edge.
	m.width = 20
	narrow := ansi.Strip(m.sessionRowView())
	if strings.Contains(narrow, "Deploy") {
		t.Fatalf("a row with no room kept the name: %q", narrow)
	}
	if !strings.HasSuffix(strings.TrimRight(narrow, " "), "⚑") {
		t.Fatalf("the marks left the right edge when the name went: %q", narrow)
	}

	if strings.Contains(ansi.Strip(m.statusView()), "Deploy the cluster") {
		t.Fatalf("the bottom bar still repeats the session name: %q", ansi.Strip(m.statusView()))
	}
	if m.styles.sessionName.GetForeground() == everforest.Blue {
		t.Fatal("the session name is still the loud blue")
	}
}

func TestSessionRowShowsParentBelowSidecarName(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width = 100
	m.session = store.SessionMeta{
		ID: uuid.New(), ParentID: uuid.New(), Name: "Build worker", AgentKind: contracts.SidecarLoopAgent,
	}
	m.parentName = "Orchestrator"
	m.parentAgentKind = contracts.InteractiveAgent

	rows := strings.Split(ansi.Strip(m.sessionRowView()), "\n")
	var sessionRow, parentRow int = -1, -1
	for i, row := range rows {
		if strings.Contains(row, "Build worker") {
			sessionRow = i
		}
		if strings.Contains(row, "↖ ⬢ Orchestrator") {
			parentRow = i
		}
	}
	if sessionRow == -1 || parentRow != sessionRow+1 {
		t.Fatalf("parent row is not directly below the sidecar row: %q", rows)
	}
}

func TestArrowKeysNavigateRelatedSessionsAcrossHITL(t *testing.T) {
	parentID, firstChild, secondChild := uuid.New(), uuid.New(), uuid.New()
	client := &navigationChatClient{}
	m := newChatModel(context.Background(), client, nil, RunOptions{})
	m.modal = nil
	m.width = 100
	m.session = store.SessionMeta{ID: uuid.New(), ParentID: parentID, Name: "worker"}
	m.parentName = "Orchestrator"
	m.parentAgentKind = contracts.InteractiveAgent
	m.sidecars = []contracts.SidecarStatus{{ID: firstChild, Name: "first"}, {ID: secondChild, Name: "second"}}
	m.composer.SetValue("draft")
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.relatedSessionSelection != uuid.Nil {
		t.Fatal("relationship navigation stole an arrow from a non-empty composer")
	}

	m.approval = &Approval{}
	m.composer.SetValue("draft preserved while approval is pending")

	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.relatedSessionSelection != parentID {
		t.Fatalf("first down selected %s, want parent %s", m.relatedSessionSelection, parentID)
	}
	highlighted := m.parentRowView()
	m.relatedSessionSelection = uuid.Nil
	plain := m.parentRowView()
	if highlighted == plain || ansi.Strip(highlighted) != ansi.Strip(plain) {
		t.Fatal("selected parent row did not receive a presentation-only highlight")
	}

	m.relatedSessionSelection = parentID
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.relatedSessionSelection != firstChild {
		t.Fatalf("second down selected %s, want first child %s", m.relatedSessionSelection, firstChild)
	}
	highlighted = m.sidecarRowsView()
	m.relatedSessionSelection = uuid.Nil
	plain = m.sidecarRowsView()
	if highlighted == plain || ansi.Strip(highlighted) != ansi.Strip(plain) {
		t.Fatal("selected sidecar row did not receive a presentation-only highlight")
	}
	m.relatedSessionSelection = firstChild
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if m.relatedSessionSelection != parentID {
		t.Fatalf("up selected %s, want parent %s", m.relatedSessionSelection, parentID)
	}
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.relatedSessionSelection != uuid.Nil {
		t.Fatalf("escape kept relationship selection %s", m.relatedSessionSelection)
	}
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.relatedSessionSelection != parentID {
		t.Fatalf("down after escape selected %s, want parent %s", m.relatedSessionSelection, parentID)
	}

	_, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("enter did not open the selected parent")
	}
	_ = cmd()
	if client.opened != parentID {
		t.Fatalf("opened session = %s, want parent %s", client.opened, parentID)
	}
}

// A caller that delegated lists its children under its own row, each with the
// marks the session list uses, so the caller shows what a sidecar is doing
// without the reader joining it.
func TestSessionRowListsSidecarsAndTheirStatus(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width = 100
	m.working = true
	m.session = store.SessionMeta{ID: uuid.New(), Name: "Orchestrator", AgentKind: contracts.InteractiveAgent}
	child := uuid.New()
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
		ID: child, Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionWaitingApproval,
	}})

	row := ansi.Strip(m.sessionRowView())
	for _, want := range []string{"explorer", "⧉", "waiting for approval", "Orchestrator"} {
		if !strings.Contains(row, want) {
			t.Fatalf("session row lacks %q: %q", want, row)
		}
	}
	if strings.Index(row, "explorer") < strings.Index(row, "Orchestrator") {
		t.Fatalf("the child is not below the caller: %q", row)
	}

	// A later report for the same child updates its one row instead of adding a
	// second, and the status follows.
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
		ID: child, Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionCompleted,
	}})
	row = ansi.Strip(m.sessionRowView())
	if got := strings.Count(row, "explorer"); got != 1 {
		t.Fatalf("child rows = %d, want 1: %q", got, row)
	}
	if !strings.Contains(row, "completed") || strings.Contains(row, "waiting for approval") {
		t.Fatalf("the child's status did not follow its report: %q", row)
	}
}

// A child's row is session state, not turn state: it stays put after the
// caller's own turn ends, because a background sidecar keeps working long
// after its caller has answered and gone idle.
// A child that has finished keeps the status it finished on. A report can land
// after the one that ended it — a refresh racing the close that superseded it —
// and letting it through would put the row back on a status the child has left
// for good, with no later report coming to correct it.
func TestAFinishedSidecarRowIsNotWalkedBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status contracts.SessionStatus
		label  string
	}{
		{"completed", contracts.SessionCompleted, "completed"},
		{"exhausted", contracts.SessionMaxIterationExhausted, "max iterations reached"},
		{"tombstone", contracts.SessionTombstone, "inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChatModel(context.Background(), nil, nil, RunOptions{})
			m.modal = nil
			m.width = 100
			m.session = store.SessionMeta{ID: uuid.New(), Name: "Orchestrator", AgentKind: contracts.InteractiveAgent}
			child := uuid.New()
			report := func(status contracts.SessionStatus) {
				m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
					ID: child, Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: status,
				}})
			}

			report(contracts.SessionBusy)
			report(tc.status)
			report(contracts.SessionBusy) // superseded: the child has already finished

			row := ansi.Strip(m.sessionRowView())
			if !strings.Contains(row, "explorer") {
				t.Fatalf("the child left the row entirely: %q", row)
			}
			if !strings.Contains(row, tc.label) {
				t.Fatalf("the row did not keep %q: %q", tc.label, row)
			}
			if strings.Contains(row, "busy") {
				t.Fatalf("a finished child's row was walked back: %q", row)
			}
		})
	}
}

func TestSidecarRowsPersistWhenTheCallerGoesIdle(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width = 100
	m.working = true
	m.session = store.SessionMeta{ID: uuid.New(), Name: "Orchestrator", AgentKind: contracts.InteractiveAgent}
	child := uuid.New()
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
		ID: child, Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionBusy,
	}})

	// The caller reports idle while the sidecar is still running.
	m.applySessionStatus(contracts.SessionIdle)

	row := ansi.Strip(m.sessionRowView())
	if !strings.Contains(row, "explorer") || !strings.Contains(row, "busy") {
		t.Fatalf("the child left the row when the caller went idle: %q", row)
	}

	// A later report updates that same row in place.
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
		ID: child, Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionWaitingApproval,
	}})
	row = ansi.Strip(m.sessionRowView())
	if got := strings.Count(row, "explorer"); got != 1 {
		t.Fatalf("child rows = %d, want 1: %q", got, row)
	}
	if !strings.Contains(row, "waiting for approval") || strings.Contains(row, "busy") {
		t.Fatalf("the child's status did not follow its report: %q", row)
	}
}

// A child row belongs to the session that spawned it. Rejoining that session —
// the session list opening and closing, or a subscriber gap — leaves the rows
// alone, and opening another session takes them away, because the switch loads
// the new session's metadata before its view arrives.
func TestSidecarRowsBelongToTheSessionThatSpawnedThem(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.modal = nil
	m.width = 100
	m.session = store.SessionMeta{ID: uuid.New(), Name: "Orchestrator", AgentKind: contracts.InteractiveAgent}
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventSidecarStatus, Sidecar: &contracts.SidecarStatus{
		ID: uuid.New(), Name: "explorer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionBusy,
	}})

	// Rejoining the same session keeps its children.
	m.applySessionView(glue.SessionView{Meta: m.session, Status: contracts.SessionIdle})
	if row := ansi.Strip(m.sessionRowView()); !strings.Contains(row, "explorer") {
		t.Fatalf("rejoining the same session dropped its children: %q", row)
	}

	// Opening another session runs the load message first, which is where the
	// session's metadata is replaced; the view for it follows.
	other := store.SessionMeta{ID: uuid.New(), Name: "Elsewhere", AgentKind: contracts.InteractiveAgent}
	if _, _ = m.Update(sessionLoadedMsg{output: &glue.SessionOutput{SessionMeta: other}}); false {
		t.Fatal("unreachable")
	}
	m.applySessionView(glue.SessionView{Meta: other, Status: contracts.SessionIdle})

	if row := ansi.Strip(m.sessionRowView()); strings.Contains(row, "explorer") {
		t.Fatalf("another session inherited the last one's children: %q", row)
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

func TestStreamingBlocksRenderMarkdownOnlyWhenComplete(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})

	m.appendDelta(contracts.DeltaReasoning, "Use **careful reasoning**.")
	m.refreshTranscript(true)
	if content := ansi.Strip(m.viewport.View()); !strings.Contains(content, "**careful reasoning**") {
		t.Fatalf("active thinking block was rendered as Markdown: %q", content)
	}
	// The in-flight block is cached like any other, so the cache has to be
	// dropped as its text grows; a stale render would drop the new delta.
	m.appendDelta(contracts.DeltaReasoning, " More text.")
	m.refreshTranscript(true)
	if content := ansi.Strip(m.viewport.View()); !strings.Contains(content, "More text.") {
		t.Fatalf("in-flight block kept a stale render: %q", content)
	}

	m.appendDelta(contracts.DeltaContent, "Final **answer**.")
	m.refreshTranscript(true)
	content := ansi.Strip(m.viewport.View())
	if strings.Contains(content, "**careful reasoning**") {
		t.Fatalf("completed thinking block was not rendered as Markdown: %q", content)
	}
	if !strings.Contains(content, "Final **answer**.") {
		t.Fatalf("active assistant block was rendered as Markdown: %q", content)
	}

	cachedThinking := m.blocks[0].rendered
	m.appendDelta(contracts.DeltaContent, " And more.")
	m.refreshTranscript(true)
	if m.blocks[0].rendered != cachedThinking {
		t.Fatal("completed thinking block was rendered again during assistant streaming")
	}

	// The session reporting idle is what ends the turn on the live path.
	m.session.ID = uuid.New()
	m.sessionCancel = func() {}
	idle := string(contracts.SessionIdle)
	_, _ = m.Update(sessionEventMsg{
		sessionID:  m.session.ID,
		observerID: m.sessionObserverID,
		event:      contracts.EventStream{Kind: contracts.EventSessionTransitionState, Content: &idle},
	})
	content = ansi.Strip(m.viewport.View())
	if strings.Contains(content, "**answer**") || !m.blocks[1].renderedValid {
		t.Fatalf("assistant block was not finalized as Markdown: %q", content)
	}
}

// observerEvent is one session event as the observer delivers it: the path a
// running turn's model output takes to the transcript.
func observerEvent(m *chatModel, kind contracts.EventStreamKind, content string) tea.Msg {
	return sessionEventMsg{
		sessionID:  m.session.ID,
		observerID: m.sessionObserverID,
		event:      contracts.EventStream{Kind: kind, Content: &content},
	}
}

// A tool call ends the answer that asked for it. The block must stop streaming
// there, so its markdown renders while the tool runs rather than staying raw
// until the next step's first token.
func TestToolCallFinalizesTheAnswerItFollows(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session.ID = uuid.New()
	m.sessionCancel = func() {}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})

	_, _ = m.Update(observerEvent(m, contracts.DeltaContent, "Audit **done**."))
	call := contracts.ToolCall{
		ID: "call-1", Type: "function",
		Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"ls"}`},
	}
	_, _ = m.Update(sessionEventMsg{
		sessionID:  m.session.ID,
		observerID: m.sessionObserverID,
		event:      contracts.EventStream{Kind: contracts.EventToolCall, ToolCall: &call},
	})

	content := ansi.Strip(m.viewport.View())
	if strings.Contains(content, "**done**") {
		t.Fatalf("the answer is still showing its raw source after its tool call: %q", content)
	}
	if !strings.Contains(content, "Audit done.") {
		t.Fatalf("the answer is missing from the transcript: %q", content)
	}
}

// A reader who scrolled up keeps their place while the turn keeps producing:
// live output follows the newest line only when the view is already there.
func TestStreamingLeavesAReaderWhoScrolledUpWhereTheyAre(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session.ID = uuid.New()
	m.sessionCancel = func() {}
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.scrollTranscript(-3 * m.transcriptHeight())
	if m.atBottom() {
		t.Fatal("scrolling up left the view pinned to the newest output")
	}
	anchored := m.anchor

	_, _ = m.Update(observerEvent(m, contracts.DeltaContent, "zzz-newest-token"))
	if m.anchor != anchored {
		t.Fatalf("a stream event moved the view from %+v to %+v", anchored, m.anchor)
	}
	if m.atBottom() {
		t.Fatal("the status row would stop saying the reader is on earlier output")
	}
	if content := ansi.Strip(m.viewport.GetContent()); strings.Contains(content, "zzz-newest-token") {
		t.Fatalf("a stream event scrolled the reader to the newest output:\n%s", content)
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, "zzz-newest-token") {
		t.Fatalf("the token did not land in the block: %q", last.text)
	}
}

// The reader who is at the newest line keeps following it, and scrolling back
// down resumes following.
func TestStreamingFollowsTheNewestLine(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session.ID = uuid.New()
	m.sessionCancel = func() {}
	for i := range 200 {
		m.blocks = append(m.blocks, block{role: "assistant", text: fmt.Sprintf("## Block %d", i)})
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	_, _ = m.Update(observerEvent(m, contracts.DeltaContent, "followed-token"))
	if !m.atBottom() {
		t.Fatal("a stream event stopped a reader who was at the newest line")
	}
	if content := ansi.Strip(m.viewport.GetContent()); !strings.Contains(content, "followed-token") {
		t.Fatalf("the newest output is not on screen:\n%s", content)
	}

	m.scrollTranscript(-m.transcriptHeight())
	_, _ = m.Update(observerEvent(m, contracts.DeltaContent, " while-away"))
	if m.atBottom() {
		t.Fatal("a stream event re-pinned a reader who had scrolled up")
	}

	m.scrollTranscript(m.transcriptHeight() * 4)
	if !m.atBottom() {
		t.Fatal("scrolling to the end did not reach the newest output")
	}
	_, _ = m.Update(observerEvent(m, contracts.DeltaContent, " resumed"))
	if content := ansi.Strip(m.viewport.GetContent()); !strings.Contains(content, "resumed") {
		t.Fatalf("following did not resume after scrolling back down:\n%s", content)
	}
}

// Model output is accepted as it arrives: each event draws on the spot, so the
// screen always shows the newest text with no interval in between.
func TestStreamingEventsRenderAsTheyArrive(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session.ID = uuid.New()
	m.sessionCancel = func() {}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	for _, token := range []string{"first ", "second ", "third"} {
		_, _ = m.Update(observerEvent(m, contracts.DeltaContent, token))
		if content := ansi.Strip(m.viewport.View()); !strings.Contains(content, strings.TrimSpace(token)) {
			t.Fatalf("token %q was not drawn on arrival: %q", token, content)
		}
	}
	if !strings.Contains(m.blocks[0].text, "first second third") {
		t.Fatalf("tokens were not accumulated in the block: %q", m.blocks[0].text)
	}
}

func TestTranscriptUsesCompactRoleMarkers(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{
		{role: "user", text: "question"},
		{role: "thinking", text: "reasoning"},
		{role: "skill", text: "green-software"},
		{role: "tool", toolKind: "call", toolName: "search", text: `search {"path":"."}`, toolStatus: "success"},
		{role: "assistant", text: "answer"},
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"● question", "◌ reasoning", "✦ green-software", `▲ search {"path":"."}`, "● answer"} {
		if !strings.Contains(content, want) {
			t.Fatalf("chat transcript does not contain %q", want)
		}
	}
	// One block per call: the result rides under the call it answers, so no
	// result of its own is drawn.
	for _, unwanted := range []string{"YOU", "THINKING", "Skill loaded:", "tool call:", "tool result:", "▼"} {
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

func TestApprovalCompactPreviewFormatsFileTools(t *testing.T) {
	tests := []struct {
		name      contracts.ToolType
		arguments string
		want      string
	}{
		{contracts.ReadTool, `{"path":"main.go","offset":10,"limit":4}`, "main.go  (lines 10-13)"},
		{contracts.WriteTool, `{"path":"notes.txt","content":"first\nsecond"}`, "notes.txt  (2 lines, 12 bytes)"},
		{contracts.EditTool, `{"path":"main.go","old_string":"old\nsame","new_string":"new\nsame","replace_all":true}`, "main.go  (every match; 2→2 lines, 8→8 bytes)"},
	}
	for _, tt := range tests {
		call := contracts.ToolCall{Function: contracts.Function{Name: tt.name, Arguments: tt.arguments}}
		if got := approvalCompactPreview(call); got != tt.want {
			t.Fatalf("%s preview = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHitlViewUsesCompactEditPreview(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.width = 180
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name: contracts.EditTool,
		Arguments: `{"path":"demo/workload_demov1.yaml","old_string":"apiVersion: apps/v1\nkind: Deployment\nmetadata:",` +
			`"new_string":"# probe\napiVersion: apps/v1\nkind: Deployment\nmetadata:"}`,
	}}})

	view := ansi.Strip(m.hitlView())
	for _, want := range []string{"edit", "demo/workload_demov1.yaml", "3→4 lines", "[A]llow"} {
		if !strings.Contains(view, want) {
			t.Fatalf("HITL view lacks %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"diff --git", "--- a/", "+++ b/", "@@"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("HITL compact view contains %q:\n%s", unwanted, view)
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

// An edit preview is built from a snippet, so it knows where the text is inside
// the snippet and nowhere else. It must not print line numbers: the client
// cannot see the file they would have to come from, and a snippet's coordinates
// shown as file lines read as a wrong location.
func TestRenderEditDiffBlockDrawsNoFileLineNumbers(t *testing.T) {
	styles := newHarnessStyles()
	args := `{"path":"main.go","old_string":"return old","new_string":"return new"}`
	rendered := ansi.Strip(renderEditDiffBlock("▲", styles.system, styles, args, 70))
	for _, want := range []string{"edit  main.go", "- return old", "+ return new"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("edit diff lacks %q:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"@@", "1   -", "  1 +"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("edit diff shows snippet coordinates %q:\n%s", unwanted, rendered)
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
	before := ansi.Strip(m.viewport.GetContent())
	_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{X: 1, Y: 2, Button: tea.MouseWheelUp}))

	if after := ansi.Strip(m.viewport.GetContent()); after == before {
		t.Fatalf("wheel up did not move the transcript:\n%s", after)
	}
	if m.atBottom() {
		t.Fatal("wheel up left the view pinned to the newest output")
	}
}

func TestSidecarChatIsReadOnly(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session = store.SessionMeta{ID: uuid.New(), AgentKind: contracts.SidecarLoopAgent}
	m.working = true
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if m.composer.Value() != "" {
		t.Fatalf("sidecar accepted composer input: %q", m.composer.Value())
	}
	m.composer.SetValue("try to send")
	if cmd := m.submit(); cmd != nil {
		t.Fatal("sidecar dispatched a chat message")
	}
	if cmd := m.runCommand("/compact"); cmd != nil {
		t.Fatal("sidecar dispatched manual compaction")
	}
	if cmd := m.runCommand("/model"); cmd != nil {
		t.Fatal("sidecar opened the model picker")
	}
}

func TestSidecarHierarchyInSessionList(t *testing.T) {
	parent, child := uuid.New(), uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: child, ParentID: parent, Name: "Worker", AgentKind: contracts.SidecarLoopAgent},
		{ID: parent, Name: "Caller", AgentKind: contracts.InteractiveAgent},
	}, false)
	if len(m.modal.options) != 3 || m.modal.options[1].session != parent || m.modal.options[2].session != child {
		t.Fatalf("session order = %+v, want caller then sidecar", m.modal.options)
	}
	if m.modal.options[2].tree != "└─ " || m.modal.options[2].detailTree != "   " {
		t.Fatalf("sidecar tree = (%q, %q), want child connector and aligned detail", m.modal.options[2].tree, m.modal.options[2].detailTree)
	}
}

func TestSidecarApprovalShowsAcceptanceScript(t *testing.T) {
	call := contracts.ToolCall{Function: contracts.Function{
		Name:      contracts.SpawnSidecarLoopTool,
		Arguments: `{"agent_name":"Worker","task":"Check the build","acceptance_script":"go build ./...\necho done","max_turns":3}`,
	}}
	body, script := formatApprovalToolCall(call)
	for _, want := range []string{"SIDECAR  Worker", "TURN BUDGET  3", "TASK\nCheck the build", "ACCEPTANCE SCRIPT (must be read-only)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("approval body lacks %q: %q", want, body)
		}
	}
	if script != "go build ./...\necho done" {
		t.Fatalf("script = %q, want unescaped Bash source", script)
	}
	if preview := approvalCompactPreview(call); !strings.Contains(preview, "Worker") || !strings.Contains(preview, "acceptance script") {
		t.Fatalf("compact preview = %q", preview)
	}
}

// The badge list the session screen used on small terminals is gone: the
// workspace is the only session screen, so a narrow terminal gets the same
// sections and the same status glyphs rather than a different view.
func TestSessionScreenAlwaysUsesTheWorkspace(t *testing.T) {
	busy := uuid.New()
	closed := uuid.New()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.session.ID = busy
	m.showSessions([]contracts.SessionSummary{
		{ID: busy, Model: "active-model", Cwd: "/active", Status: contracts.SessionBusy},
		{ID: closed, Model: "saved-model", Cwd: "/saved", Status: contracts.SessionTombstone},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"SESSION WORKSPACE", "NEW SESSION", "SESSIONS", "◐", "busy", "active-model"} {
		if !strings.Contains(content, want) {
			t.Fatalf("session screen does not contain %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "[INACTIVE]") || strings.Contains(content, "[BUSY]") {
		t.Fatalf("session screen fell back to the badge list:\n%s", content)
	}
}

func TestSessionWorkspaceShowsBrandAndSections(t *testing.T) {
	active := uuid.New()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.session.ID = active
	m.showSessions([]contracts.SessionSummary{
		{ID: active, Model: "active-model", Cwd: "/active", Status: contracts.SessionBusy},
		{ID: uuid.New(), Model: "saved-model", Cwd: "/saved", Status: contracts.SessionTombstone},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"INFAI HARNESS", "SESSION WORKSPACE", "NEW SESSION", "SESSIONS", "◐", "busy", "·", "inactive", "active-model", "saved-model"} {
		if !strings.Contains(content, want) {
			t.Fatalf("session workspace does not contain %q:\n%s", want, content)
		}
	}
	if width := lipgloss.Width(m.View().Content); width > 120 {
		t.Fatalf("session workspace width=%d exceeds terminal", width)
	}
	if height := lipgloss.Height(m.View().Content); height > 30 {
		t.Fatalf("session workspace height=%d exceeds terminal", height)
	}
}

// A session blocked on a human decision must be recognisable from the list, and
// the identifying fields must survive a narrow terminal: the working directory
// goes before the model and the time, the words go before the glyph.
func TestSessionWorkspaceMarksApprovalAndDropsDetailWhenNarrow(t *testing.T) {
	waiting := uuid.New()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{{
		ID: waiting, Name: "Deploy the cluster", Model: "deepseek-v4-flash",
		Cwd: "/home/dipankardas/ws/infai", UpdatedAt: time.Now(), Status: contracts.SessionWaitingApproval,
	}}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	wide := ansi.Strip(m.View().Content)
	for _, want := range []string{"Deploy the cluster", "⚑ waiting for approval", "deepseek-v4-flash", "/home/dipankardas/ws/infai"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide session list lacks %q:\n%s", want, wide)
		}
	}

	_, _ = m.Update(tea.WindowSizeMsg{Width: 62, Height: 30})
	narrow := ansi.Strip(m.View().Content)
	for _, want := range []string{"Deploy the cluster", "⚑ waiting for approval", "deepseek-v4-flash"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow session list lacks %q:\n%s", want, narrow)
		}
	}
	if strings.Contains(narrow, "/home/dipankardas/ws/infai") {
		t.Fatalf("narrow session list kept the working directory:\n%s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if width := lipgloss.Width(line); width > 62 {
			t.Fatalf("narrow session list line width=%d exceeds terminal:\n%s", width, narrow)
		}
	}
}

func TestSessionListShowsAgentKind(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: uuid.New(), Name: "main", Model: "deepseek-v4-flash", Cwd: "/ws/infai", UpdatedAt: time.Now(),
			Status: contracts.SessionIdle, Active: true, AgentKind: contracts.InteractiveAgent},
		{ID: uuid.New(), Name: "worker", Model: "gemma4-e2b-it", Cwd: "/ws/infai", UpdatedAt: time.Now(),
			Status: contracts.SessionIdle, Active: true, AgentKind: contracts.SidecarLoopAgent},
		{ID: uuid.New(), Name: "turn", Model: "gpt-5.6-sol", Cwd: "/ws/infai", UpdatedAt: time.Now(),
			Status: contracts.SessionCompleted, AgentKind: contracts.SingleLoopAgent},
		{ID: uuid.New(), Name: "lane", Model: "gpt-5.6-luna", Cwd: "/ws/infai", UpdatedAt: time.Now(),
			Status: contracts.SessionWaitingApproval, Active: true, AgentKind: contracts.SwarmAgent},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})

	// Each kind names itself in its own group at the end of the name row, ahead
	// of the status group.
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"⬢ interactive  ○ idle",
		"⧉ sidecar_loop  ○ idle",
		"↻ loop  ✓ completed",
		"⇶ swarm  ⚑ waiting for approval",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("session list lacks the kind group %q:\n%s", want, content)
		}
	}

	// The words are a luxury and the name is not: a row shows the words only
	// while it can also show the name whole. So two rows in one list can differ,
	// and the one with the longer name keeps its own room instead of spending it
	// on a word.
	m.showSessions([]contracts.SessionSummary{
		{ID: uuid.New(), Name: "main", Model: "deepseek-v4-flash", Cwd: "/ws/infai", UpdatedAt: time.Now(),
			Status: contracts.SessionIdle, Active: true, AgentKind: contracts.InteractiveAgent},
		{ID: uuid.New(), Name: "I want you to help me Why this happened? point is when", Model: "gemma4-e2b-it",
			Cwd: "/ws/infai", UpdatedAt: time.Now(), Status: contracts.SessionBusy, Active: true,
			AgentKind: contracts.SidecarLoopAgent},
	}, false)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 62, Height: 26})

	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "⬢ interactive  ○ idle") {
		t.Fatalf("a name with room to spare did not get the words:\n%s", content)
	}
	if strings.Contains(content, "⧉ sidecar_loop") {
		t.Fatalf("a long name spent its room on a word:\n%s", content)
	}
	if !strings.Contains(content, "⧉  ◐") {
		t.Fatalf("a long name lost the kind glyph too:\n%s", content)
	}
}

func TestSessionListDeletesOnlyAfterASecondKey(t *testing.T) {
	id := uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{{ID: id, Name: "scratch", Status: contracts.SessionTombstone}}, true)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.modal.selected = 1

	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if cmd != nil {
		t.Fatal("the first d dispatched a delete instead of arming one")
	}
	if m.modal.pendingDelete != id {
		t.Fatalf("first d left pendingDelete=%v, want %v", m.modal.pendingDelete, id)
	}
	armed := ansi.Strip(m.View().Content)
	for _, want := range []string{"delete?", "press d again to permanently delete this session and its sidecars", "active work stops"} {
		if !strings.Contains(armed, want) {
			t.Fatalf("armed list lacks %q:\n%s", want, armed)
		}
	}

	_, cmd = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if cmd == nil {
		t.Fatal("the second d did not dispatch the delete")
	}
	actioned, ok := cmd().(sessionActionedMsg)
	if !ok || actioned.action != "delete" || actioned.id != id {
		t.Fatalf("second d dispatched %#v, want a delete of %v", actioned, id)
	}
	if m.modal.pendingDelete != uuid.Nil {
		t.Fatal("a confirmed delete stayed armed")
	}
}

func TestSessionListAnyOtherKeyCancelsAnArmedDelete(t *testing.T) {
	id := uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: id, Name: "scratch", Status: contracts.SessionTombstone},
		{ID: uuid.New(), Name: "kept", Status: contracts.SessionIdle},
	}, true)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.modal.selected = 1

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if m.modal.pendingDelete != uuid.Nil {
		t.Fatal("moving the cursor left a delete armed")
	}

	// The next d arms this row rather than deleting the previously armed one.
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if cmd != nil {
		t.Fatal("a key after the arm was read as the confirmation")
	}
	if m.modal.pendingDelete == id {
		t.Fatal("the wrong row was armed after the cursor moved")
	}

	// A close answers the arm rather than leaving it behind, so a later d on
	// the same row still has to arm again.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Text: "c"}))
	_, cmd = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if cmd != nil {
		t.Fatal("an armed delete survived the close that answered it")
	}
}

func TestDeleteRefreshesSessionsFromServer(t *testing.T) {
	parent, child, other := uuid.New(), uuid.New(), uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: parent, Name: "caller"},
		{ID: child, ParentID: parent, Name: "sidecar"},
		{ID: other, Name: "other"},
	}, false)
	_, cmd := m.Update(sessionActionedMsg{action: "delete", id: parent})
	if cmd == nil {
		t.Fatal("successful delete did not request refreshed sessions")
	}
	if _, ok := cmd().(sessionsListedMsg); !ok {
		t.Fatal("delete did not call ListSessions")
	}
	_, _ = m.Update(sessionsListedMsg{sessions: []contracts.SessionSummary{{ID: other, Name: "other"}}})
	if len(m.modal.options) != 2 || m.modal.options[1].session != other {
		t.Fatalf("stale offspring remained in refreshed list: %+v", m.modal.options)
	}
}

func TestFailedDeleteDoesNotRefreshSessions(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{{ID: uuid.New(), Name: "saved"}}, false)
	_, cmd := m.Update(sessionActionedMsg{action: "delete", err: fmt.Errorf("failed")})
	if cmd != nil || m.modal == nil || m.modal.kind != modalNotice {
		t.Fatal("failed delete should show error without refreshing")
	}
}

func TestSessionListCloseAndDeleteUpdateTheRow(t *testing.T) {
	open, saved := uuid.New(), uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: open, Name: "open one", Status: contracts.SessionIdle, Active: true},
		{ID: saved, Name: "saved one", Status: contracts.SessionTombstone},
	}, true)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	m.modal.selected = 1
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Text: "c"}))
	if cmd == nil {
		t.Fatal("c did not dispatch a close")
	}
	closed, ok := cmd().(sessionActionedMsg)
	if !ok || closed.action != "close" || closed.id != open {
		t.Fatalf("c dispatched %#v, want a close of %v", closed, open)
	}
	_, _ = m.Update(closed)
	if m.modal.options[1].sessionStatus != contracts.SessionTombstone {
		t.Fatalf("closed session still reads %q", m.modal.options[1].sessionStatus)
	}

	// Deleting removes the row, keeps the cursor on the row that took its place,
	// and detaches this client when the deleted session was the attached one.
	m.session = store.SessionMeta{ID: open, Model: "test-model"}
	_, _ = m.Update(sessionActionedMsg{action: "delete", id: open})
	if len(m.modal.options) != 2 {
		t.Fatalf("options=%d after a delete, want the new-session row and one session", len(m.modal.options))
	}
	if m.modal.options[1].session != saved {
		t.Fatal("the wrong row was deleted")
	}
	if m.modal.selected != 1 {
		t.Fatalf("selection=%d after a delete, want the row that moved up", m.modal.selected)
	}
	if m.session.ID != uuid.Nil {
		t.Fatal("deleting the attached session did not detach the client")
	}
}

func TestSessionListCloseOnlyAppliesToASessionTheEngineHolds(t *testing.T) {
	open, saved := uuid.New(), uuid.New()
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.showSessions([]contracts.SessionSummary{
		{ID: open, Name: "open one", Status: contracts.SessionIdle, Active: true},
		// A saved session is not resident, whatever status it reports.
		{ID: saved, Name: "saved one", Status: contracts.SessionTombstone},
		// A concluded session stays in the engine until it is closed, so its
		// status alone would hide that it is still open.
		{ID: uuid.New(), Name: "finished one", Status: contracts.SessionCompleted, Active: true},
	}, true)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	m.modal.selected = 1
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Text: "c"}))
	if cmd == nil {
		t.Fatal("c did not dispatch a close for a session the engine holds")
	}

	m.modal.selected = 2
	_, cmd = m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Text: "c"}))
	if cmd != nil {
		t.Fatal("c dispatched a close for a session the engine is not holding")
	}
	if notice := ansi.Strip(m.View().Content); !strings.Contains(notice, "SESSION IS NOT OPEN") {
		t.Fatalf("closing a saved session did not explain itself:\n%s", notice)
	}
}

// "idle" and "inactive" are the two statuses a list is mostly made of, and the
// whole difference between them is that one is open and the other is not. That
// has to reach the terminal as colour, because the words alone do not carry it.
func TestSessionStatusesReadDifferently(t *testing.T) {
	styles := newHarnessStyles()
	foregrounds := map[contracts.SessionStatus]color.Color{}
	for _, status := range []contracts.SessionStatus{
		contracts.SessionIdle, contracts.SessionBusy, contracts.SessionWaitingApproval, contracts.SessionCompacting,
		contracts.SessionCompleted, contracts.SessionMaxIterationExhausted, contracts.SessionTombstone,
	} {
		foregrounds[status] = describeSessionStatus(status, styles).style.GetForeground()
	}

	for status, want := range map[contracts.SessionStatus]color.Color{
		contracts.SessionIdle:                  everforest.Green,
		contracts.SessionBusy:                  everforest.Yellow,
		contracts.SessionWaitingApproval:       everforest.Orange,
		contracts.SessionCompacting:            everforest.Yellow,
		contracts.SessionCompleted:             everforest.Aqua,
		contracts.SessionMaxIterationExhausted: everforest.Red,
		contracts.SessionTombstone:             everforest.Muted,
	} {
		if got := foregrounds[status]; got != want {
			t.Errorf("status %s foreground=%v want=%v", status, got, want)
		}
	}
	if foregrounds[contracts.SessionIdle] == foregrounds[contracts.SessionTombstone] {
		t.Fatal("an open session and a closed one are drawn in the same colour")
	}
	if foregrounds[contracts.SessionIdle] == foregrounds[contracts.SessionCompleted] {
		t.Fatal("an open session and a finished one are drawn in the same colour")
	}
}

func TestWorkingTurnQueuesInputWithoutReplacingStatus(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.working = true
	m.workStatus = "waiting for approval"
	m.session = store.SessionMeta{ID: uuid.New(), Model: "test-model"}
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	_, _ = m.Update(tea.PasteMsg{Content: "queued"})
	if m.composer.Value() != "xqueued" {
		t.Fatalf("composer while working = %q, want the typed text", m.composer.Value())
	}

	// A queued prompt must leave the running turn's status to the events.
	if cmd := m.submit(); cmd == nil {
		t.Fatal("prompt sent while working was dropped")
	}
	if m.workStatus != "waiting for approval" {
		t.Fatalf("queued prompt replaced the running status with %q", m.workStatus)
	}
	if !m.working {
		t.Fatal("queued prompt ended the running turn")
	}
	if m.composer.Value() != "" {
		t.Fatalf("queued prompt left the composer holding %q", m.composer.Value())
	}
}

func TestWorkingTurnOpensSessionsWithoutCancelingTurn(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.modal = nil
	m.session.ID = uuid.New()
	m.working = true
	m.workStatus = "working"
	m.status = contracts.SessionBusy

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	if m.modal == nil || !m.working || m.status != contracts.SessionBusy {
		t.Fatal("opening sessions changed the running turn")
	}
	if m.sessionCancel != nil {
		t.Fatal("opening sessions left the observer attached")
	}
	_, _ = m.Update(sessionsListedMsg{})
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil || m.modal != nil || m.sessionCancel == nil {
		t.Fatal("dismissing sessions did not resume observation")
	}

	escape := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	_, cmd = m.Update(escape)
	if cmd == nil || !m.cancelArmed {
		t.Fatal("first escape did not arm cancellation timeout")
	}
	_, cmd = m.Update(escape)
	if cmd == nil || m.workStatus != "canceling" {
		t.Fatal("second escape did not request turn cancellation")
	}
}

func TestWorkingTurnCancelArmExpires(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.working = true
	m.workStatus = "working"

	escape := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	_, _ = m.Update(escape)
	armID := m.cancelArmID
	_, _ = m.Update(cancelArmTimeoutMsg{id: armID})
	if m.cancelArmed {
		t.Fatal("escape cancellation remained armed after timeout")
	}
	if m.workStatus != "working" {
		t.Fatalf("work status after timeout = %q, want working", m.workStatus)
	}

	_, cmd := m.Update(escape)
	if cmd == nil || !m.cancelArmed {
		t.Fatal("escape after timeout did not start a new cancellation sequence")
	}
	_, _ = m.Update(cancelArmTimeoutMsg{id: armID})
	if !m.cancelArmed {
		t.Fatal("stale timeout disarmed a newer escape sequence")
	}
}

func TestNormalizeMarkdownMath(t *testing.T) {
	input := `Operational carbon is $\text{EnergyKWh} \times \mathrm{Intensity}$ and $\frac{a}{b}$.`
	want := "Operational carbon is EnergyKWh × Intensity and (a)/(b)."
	if got := normalizeMarkdownMath(input); got != want {
		t.Fatalf("normalized math=%q want=%q", got, want)
	}
}

func TestSelectionScreenStaysWithinTerminal(t *testing.T) {
	options := make([]modalOption, 30)
	for i := range options {
		options[i] = modalOption{label: strings.Repeat("long option ", 8)}
	}
	m := &modalModel{
		kind:     modalModels,
		title:    "Approval",
		body:     "Choose one",
		options:  options,
		selected: len(options) - 1,
	}
	rendered := renderSelectionScreen(m, 40, 10, newHarnessStyles())
	if width := lipgloss.Width(rendered); width != 40 {
		t.Fatalf("screen width=%d want 40", width)
	}
	if height := lipgloss.Height(rendered); height != 10 {
		t.Fatalf("screen height=%d want 10", height)
	}

	areas := layoutRows(20, 2, intrinsic("header"), fill(), intrinsic("status"), intrinsic("composer"))
	if got := sumAreaHeights(areas); got != 2 {
		t.Fatalf("short layout height=%d want=2", got)
	}
}

func TestApprovalBandKeepsTranscriptVisible(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = []block{{role: "system", text: "transcript remains visible"}}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	m.showApproval(&Approval{})

	collapsed := ansi.Strip(m.View().Content)
	for _, want := range []string{"transcript remains visible", "TOOL", "[A]llow", "[D]eny"} {
		if !strings.Contains(collapsed, want) {
			t.Fatalf("approval view does not contain %q:\n%s", want, collapsed)
		}
	}
	// The detail arrives with the expansion, into the transcript that keeps its
	// content visible behind it.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	expanded := ansi.Strip(m.View().Content)
	for _, want := range []string{"transcript remains visible", "Human In the Loop", "tool_call: TOOL", "[A]llow", "[D]eny"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded approval view does not contain %q:\n%s", want, expanded)
		}
	}
}

func TestApprovalReservesOneLineAndExpandsIntoTheTranscript(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      contracts.BashTool,
			Arguments: `{"command":"rm -rf ./build\nmake all","workdir":"/w","timeout":30}`,
		},
	}})

	// Collapsed: one reserved line that carries the answers and hides the body.
	collapsed := ansi.Strip(m.View().Content)
	for _, want := range []string{"bash", "[A]llow", "[D]eny", "[R]eason", "ctrl+g"} {
		if !strings.Contains(collapsed, want) {
			t.Fatalf("pending block lacks %q:\n%s", want, collapsed)
		}
	}
	// The answers get their own row rather than crowding the preview.
	if !strings.Contains(collapsed, "\n   [A]llow   [D]eny   [R]eason") {
		t.Fatalf("answers are not on their own row:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "WORKING DIRECTORY") {
		t.Fatalf("collapsed approval already shows the body:\n%s", collapsed)
	}

	// Expanded: the detail joins the transcript, which owns the scrolling.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	expanded := ansi.Strip(m.View().Content)
	for _, want := range []string{"WORKING DIRECTORY  /w", "[A]llow", "[D]eny", "ctrl+g"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded approval lacks %q:\n%s", want, expanded)
		}
	}
	if height := lipgloss.Height(m.View().Content); height != 24 {
		t.Fatalf("approval frame height=%d want 24", height)
	}
}

func TestApprovalKeysAnswerTheDecision(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	approval := &Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"ls"}`}}}
	m.showApproval(approval)

	// A pending decision owns the keyboard: a stray letter must not reach the
	// composer, where it could look like a prompt.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if m.composer.Value() != "" {
		t.Fatalf("pending approval let %q into the composer", m.composer.Value())
	}

	// [R]eason hands the composer to the reason and sends it with the denial.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if m.composer.Prompt != "" && !strings.Contains(m.composer.View(), "why") {
		t.Fatal("reason mode did not mark the composer")
	}
	m.composer.SetValue("delete only inside build")
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("reason was not dispatched")
	}
	if m.approval != nil {
		t.Fatal("approval still pending after a denial")
	}
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.text, "deny_with_reason") || !strings.Contains(last.text, "delete only inside build") {
		t.Fatalf("transcript recorded %q", last.text)
	}
}

func TestApprovalKeysPreserveComposerDraft(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m.composer.SetValue("queued prompt")
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"ls"}`}}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	if got := m.composer.Value(); got != "queued prompt" {
		t.Fatalf("approving cleared draft: got %q", got)
	}
}

func TestApprovalReasonRestoresComposerDraft(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	m.composer.SetValue("queued prompt")
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"ls"}`}}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if got := m.composer.Value(); got != "" {
		t.Fatalf("reason mode kept draft: got %q", got)
	}
	m.composer.SetValue("not safe")
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got := m.composer.Value(); got != "queued prompt" {
		t.Fatalf("reason submission did not restore draft: got %q", got)
	}
}

func TestApprovalDetailRendersEditDiff(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      contracts.EditTool,
			Arguments: `{"path":"main.go","old_string":"return old","new_string":"return new"}`,
		},
	}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Human In the Loop", "tool_call: edit", "TARGET  main.go", "- return old", "+ return new", "[A]llow", "[D]eny"} {
		if !strings.Contains(content, want) {
			t.Fatalf("edit approval detail lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"BEFORE", "AFTER", "@@"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("edit approval detail still shows %q:\n%s", unwanted, content)
		}
	}
}

func TestEditDiffWrapsAndKeepsActionsPinned(t *testing.T) {
	long := strings.Repeat("wrapping content ", 12)
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      contracts.EditTool,
			Arguments: `{"path":"x.md","old_string":"` + long + `","new_string":"` + long + ` changed"}`,
		},
	}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	view := m.View().Content
	if height := lipgloss.Height(view); height != 20 {
		t.Fatalf("approval view height=%d want 20", height)
	}
	content := ansi.Strip(view)
	for _, want := range []string{"[A]llow", "[D]eny"} {
		if !strings.Contains(content, want) {
			t.Fatalf("wrapped edit approval lost pinned action %q:\n%s", want, content)
		}
	}
}

func TestApprovalDetailRendersWriteAdditions(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{
		Function: contracts.Function{
			Name:      contracts.WriteTool,
			Arguments: `{"path":"notes.txt","content":"first line\nsecond line"}`,
		},
	}})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Human In the Loop", "tool_call: write", "TARGET  notes.txt", "1 + first line", "2 + second line"} {
		if !strings.Contains(content, want) {
			t.Fatalf("write approval detail lacks %q:\n%s", want, content)
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
			want:      []string{"SOURCE  pkg/agent/tui/chat.go", "lines 10-34"},
		},
		{
			name: "bash", tool: contracts.BashTool,
			arguments: `{"command":"printf 'hello\\nworld'\nprintf done","workdir":"scripts","timeout":30}`,
			want:      []string{"WORKING DIRECTORY  scripts", "TIMEOUT            30 seconds", "printf 'hello\\nworld'\nprintf done"},
		},
		{
			name: "write", tool: contracts.WriteTool,
			arguments: `{"path":"notes.txt","content":"first line\nsecond line"}`,
			want:      []string{"TARGET  notes.txt", "2 lines", "EFFECT  Replace complete file contents"},
		},
		{
			name: "edit", tool: contracts.EditTool,
			arguments: `{"path":"main.go","old_string":"old\ntext","new_string":"new\ntext","replace_all":true}`,
			want:      []string{"TARGET  main.go", "Replace every exact match"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, script := formatApprovalToolCall(contracts.ToolCall{Function: contracts.Function{Name: tt.tool, Arguments: tt.arguments}})
			formatted := body + "\n" + script
			for _, want := range tt.want {
				if !strings.Contains(formatted, want) {
					t.Fatalf("formatted approval lacks %q: %q", want, formatted)
				}
			}
		})
	}
}

func TestApprovalDetailRendersBashAsCode(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.showApproval(&Approval{ToolCall: &contracts.ToolCall{Function: contracts.Function{
		Name:      contracts.BashTool,
		Arguments: `{"command":"if test -f go.mod; then\n  go test ./...\nfi","workdir":"."}`,
	}}})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Human In the Loop", "tool_call: bash", "SCRIPT", "if test -f go.mod; then", "go test ./...", "fi"} {
		if !strings.Contains(content, want) {
			t.Fatalf("bash approval detail lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "```bash") {
		t.Fatalf("bash approval detail exposes Markdown fence:\n%s", content)
	}

	// The code sits on the one inset a diff uses, never on the attention band.
	highlighted := renderApprovalScript("nvidia-smi", 40, newHarnessStyles())
	if !strings.Contains(highlighted, "\x1b[48;2;39;46;51m") {
		t.Fatalf("bash approval script does not use the code inset: %q", highlighted)
	}
	if strings.Contains(highlighted, "\x1b[48;2;77;76;67m") {
		t.Fatalf("bash approval script is painted on the attention band: %q", highlighted)
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

// A branch timeline is a screen of its own, like the model picker: it replaces
// the transcript rather than sitting over it, and every row reads as tree,
// fork, role, and label.
func TestTimelineScreenReplacesTranscriptAndHidesEventIDs(t *testing.T) {
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
	for _, want := range []string{"BRANCH TIMELINE", "* marks the current event", "user:", "explain this branch"} {
		if !strings.Contains(content, want) {
			t.Fatalf("timeline screen does not contain %q", want)
		}
	}
	if strings.Contains(content, "transcript remains visible") {
		t.Fatal("timeline screen still composites the transcript behind it")
	}
	if strings.Contains(content, eventID.String()) || strings.Contains(content, shortID(eventID)) {
		t.Fatal("timeline screen exposes an event ID")
	}
	if !m.modal.options[0].current {
		t.Fatal("timeline head is not marked as the current event")
	}
}

func timelineUserEvent(id, parent uuid.UUID, text string) TimelineEvent {
	return TimelineEvent{ID: id, ParentID: parent, Kind: store.KindMessage, Record: &store.Record{
		Kind:    store.KindMessage,
		Message: &contracts.ChatMessage{Role: "user", Content: &text},
	}}
}

func timelineSearchFixture(t *testing.T) *chatModel {
	t.Helper()
	first, second, third := uuid.New(), uuid.New(), uuid.New()
	view := &TimelineView{Head: third, Events: []TimelineEvent{
		timelineUserEvent(first, uuid.Nil, "alpha question"),
		timelineUserEvent(second, first, "beta answer"),
		timelineUserEvent(third, second, "beta again"),
	}}
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.showTimeline(view)
	return m
}

// A search in the branch timeline never filters it: the rows stay where they
// are and the cursor lands on a hit, so the branch structure is still readable
// while searching.
func TestTimelineSearchMovesTheCursorWithoutFiltering(t *testing.T) {
	m := timelineSearchFixture(t)
	rows := len(m.modal.options)
	if rows != 3 {
		t.Fatalf("timeline rows=%d want 3", rows)
	}

	m.modal.search("beta")
	if len(m.modal.options) != rows {
		t.Fatalf("search left %d rows, want all %d", len(m.modal.options), rows)
	}
	if len(m.modal.matches) != 2 {
		t.Fatalf("matches=%v want two", m.modal.matches)
	}
	if m.modal.selected != 1 {
		t.Fatalf("first hit selected=%d want 1", m.modal.selected)
	}
	m.modal.nextMatch(1)
	if m.modal.selected != 2 {
		t.Fatalf("second hit selected=%d want 2", m.modal.selected)
	}
	m.modal.nextMatch(1)
	if m.modal.selected != 1 {
		t.Fatalf("hits do not wrap: selected=%d want 1", m.modal.selected)
	}
	m.modal.nextMatch(-1)
	if m.modal.selected != 2 {
		t.Fatalf("previous hit selected=%d want 2", m.modal.selected)
	}
	// A query with no hit leaves the cursor where it was.
	m.modal.search("nothing here")
	if len(m.modal.matches) != 0 || m.modal.selected != 2 {
		t.Fatalf("miss matches=%v selected=%d want none and 2", m.modal.matches, m.modal.selected)
	}
}

// "/" opens the prompt, typing searches as it goes, and "n" walks the hits.
// Nothing is branched: the timeline stays open until enter is pressed on a row.
func TestTimelineSearchKeys(t *testing.T) {
	m := timelineSearchFixture(t)
	press := func(keys ...tea.KeyPressMsg) {
		for _, key := range keys {
			_, _ = m.Update(key)
		}
	}

	press(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	if !m.modal.searching {
		t.Fatal("slash did not open the search prompt")
	}
	for _, r := range "beta" {
		press(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	if m.modal.query != "beta" || m.modal.selected != 1 {
		t.Fatalf("query=%q selected=%d want beta and 1", m.modal.query, m.modal.selected)
	}
	if content := m.View().Content; !strings.Contains(content, "1/2 matches") {
		t.Fatalf("timeline does not show the hit count:\n%s", content)
	}

	press(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.modal.searching {
		t.Fatal("enter did not close the search prompt")
	}
	if m.modal == nil {
		t.Fatal("enter on the search prompt branched instead of closing it")
	}
	press(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	if m.modal.selected != 2 {
		t.Fatalf("n did not walk to the next hit: selected=%d want 2", m.modal.selected)
	}
	press(tea.KeyPressMsg(tea.Key{Code: 'N', Text: "N"}))
	if m.modal.selected != 1 {
		t.Fatalf("N did not walk back to the previous hit: selected=%d want 1", m.modal.selected)
	}

	// A miss reads as no matches rather than an empty timeline.
	press(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	for _, r := range "zzz" {
		press(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	if content := m.View().Content; !strings.Contains(content, "no matches") {
		t.Fatalf("timeline does not report a miss:\n%s", content)
	}
}

func TestTimelineBranchUsesColoredUnicodeGlyph(t *testing.T) {
	rendered := renderTimelineRow(modalOption{
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
	rendered := renderTimelineRow(modalOption{
		label: "existing prompt", role: "user", tree: "└─ ", fork: "original",
	}, false, 50, newHarnessStyles())
	if strings.Contains(rendered, "original") {
		t.Fatal("timeline original path still contains a text label")
	}
}

// The tree is structure, not content: its guides and connectors recede to the
// faintest colour, while the row they connect keeps its role colour.
func TestTimelineTreeRecedesBehindTheRow(t *testing.T) {
	styles := newHarnessStyles()
	rendered := renderTimelineRow(modalOption{
		label: "hello", role: "user", tree: "│  ├─ ",
	}, false, 60, styles)
	if want := styles.screenRow.Foreground(everforest.Faint).Render("│  ├─ "); !strings.Contains(rendered, want) {
		t.Fatalf("timeline tree is not painted in the faint colour: %q", rendered)
	}
	if want := styles.screenRow.Foreground(everforest.Blue).Render("user: "); !strings.Contains(rendered, want) {
		t.Fatalf("timeline row lost its role colour: %q", rendered)
	}
}

// The session list's sidecar hierarchy is structure too: its tree recedes to
// the faintest colour while the session row keeps its own, selected or not.
func TestSessionListTreeRecedesBehindTheRow(t *testing.T) {
	styles := newHarnessStyles()
	option := modalOption{
		label: "sidecar child", tree: "├─ ", detailTree: "│  ", detailParts: []string{"model"},
		sessionStatus: contracts.SessionBusy, agentKind: contracts.SidecarLoopAgent,
	}
	for _, selected := range []bool{false, true} {
		rows := sessionEntryRows(option, selected, false, 80, styles)
		if len(rows) == 0 {
			t.Fatalf("selected=%v produced no row", selected)
		}
		rowStyle := styles.screenRow
		if selected {
			rowStyle = styles.screenSel
		}
		if want := rowStyle.Foreground(everforest.Faint).Render("├─ "); !strings.Contains(rows[0], want) {
			t.Fatalf("selected=%v session tree is not faint: %q", selected, rows[0])
		}
		detailStyle := styles.inactive
		if selected {
			detailStyle = detailStyle.Background(everforest.SelectionBg)
		}
		if want := detailStyle.Foreground(everforest.Faint).Render("│  "); !strings.Contains(rows[1], want) {
			t.Fatalf("selected=%v detail row breaks the tree guide: %q", selected, rows[1])
		}
		for i, row := range rows {
			if got := lipgloss.Width(ansi.Strip(row)); got != 80 {
				t.Fatalf("selected=%v row %d width=%d want 80", selected, i, got)
			}
		}
	}
}

func TestTimelineRoleColors(t *testing.T) {
	tests := map[string]color.Color{
		"user":        everforest.Blue,
		"assistant":   everforest.Green,
		"thinking":    everforest.Muted,
		"system":      everforest.Purple,
		"tool_call":   everforest.Purple,
		"tool_result": everforest.Orange,
		"skill":       everforest.Aqua,
	}
	for role, want := range tests {
		got := timelineRoleStyle(lipgloss.NewStyle(), role).GetForeground()
		if got != want {
			t.Errorf("role %s color=%v want=%v", role, got, want)
		}
	}
}

func TestTimelineEventDisplayUsesSupportedRoles(t *testing.T) {
	call := contracts.ToolCall{Function: contracts.Function{Name: contracts.ReadSkillTool, Arguments: `{"name":"code-review"}`}}
	displays := timelineEventDisplays(TimelineEvent{Record: &store.Record{
		Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}},
	}}, nil)
	if len(displays) != 1 || displays[0].role != "skill" || displays[0].text != "code-review" {
		t.Fatalf("timeline displays=%#v want one code-review skill", displays)
	}
}

// A tool result that happens to be a JSON object is not a checklist update.
// Only the result of a task_checklist call is, so a bash result carrying an
// object is tool output, and a checklist call that failed leaves no row at all.
func TestTimelineEventDisplaysClassifiesToolResultsByCall(t *testing.T) {
	bashCall := contracts.ToolCall{ID: "bash-1", Function: contracts.Function{Name: contracts.BashTool}}
	checkCall := contracts.ToolCall{ID: "check-1", Function: contracts.Function{Name: contracts.TaskChecklistTool}}
	caller := TimelineEvent{Record: &store.Record{
		Kind:    store.KindMessage,
		Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{bashCall, checkCall}},
	}}
	toolNames := timelineToolNames([]TimelineEvent{caller})

	result := func(callID, text string) TimelineEvent {
		return TimelineEvent{Record: &store.Record{
			Kind:    store.KindMessage,
			Message: &contracts.ChatMessage{Role: "tool", ToolCallID: callID, Content: &text},
		}}
	}
	// A bash result that is empty JSON: it decodes as a checklist state, so the
	// call it answers is the only thing that can place it.
	if displays := timelineEventDisplays(result("bash-1", "{}"), toolNames); len(displays) != 1 || displays[0].role != "tool_result" {
		t.Fatalf("bash result displays=%#v want one tool_result", displays)
	}
	// A bash result with fields the checklist state does not have.
	if displays := timelineEventDisplays(result("bash-1", `{"exit_code":0,"output":"hi"}`), toolNames); len(displays) != 1 || displays[0].role != "tool_result" {
		t.Fatalf("bash exit status displays=%#v want one tool_result", displays)
	}
	if displays := timelineEventDisplays(result("check-1", "{}"), toolNames); len(displays) != 1 || displays[0].role != "system" {
		t.Fatalf("checklist result displays=%#v want one system row", displays)
	}
	if displays := timelineEventDisplays(result("check-1", "task_checklist add requires a title"), toolNames); displays != nil {
		t.Fatalf("failed checklist result displays=%#v want none", displays)
	}
}

func TestTimelineEventDisplaysThinkingAndAnswer(t *testing.T) {
	answer := "Final answer"
	event := TimelineEvent{ID: uuid.New(), Record: &store.Record{
		Kind: store.KindMessage, Message: &contracts.ChatMessage{
			Role: "assistant", Content: &answer, ReasoningContent: "Reasoning process",
		},
	}}
	displays := timelineEventDisplays(event, nil)
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
	toolResult := contracts.NewToolMessage(call.ID, `{"content":"hello"}`, contracts.ToolExecutionSuccess)
	records := []store.Record{
		{Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}}},
		{Kind: store.KindMessage, Message: &toolResult},
	}

	blocks := blocksFromRecords(records)
	if len(blocks) != 1 {
		t.Fatalf("blocks=%d want 1: %#v", len(blocks), blocks)
	}
	if blocks[0].role != "tool" || blocks[0].text != "README.md" {
		t.Fatalf("tool call block=%#v", blocks[0])
	}
	if blocks[0].toolName != "read" || blocks[0].toolStatus != string(contracts.ToolExecutionSuccess) {
		t.Fatalf("folded result=%#v, want a successful read", blocks[0])
	}
	if blocks[0].toolDetail != "" {
		t.Fatalf("a read that worked adds %q, want nothing", blocks[0].toolDetail)
	}
}

// A bash call reads as the command and then its output, under one pyramid: the
// command is the request, and what follows it is what came back.
func TestToolBlockPutsTheOutputUnderTheCommand(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	entry := &block{
		role:       "tool",
		toolKind:   "call",
		toolName:   string(contracts.BashTool),
		toolArgs:   `{"command":"go test ./...","workdir":"scripts","timeout":30}`,
		toolStatus: string(contracts.ToolExecutionSuccess),
		toolDetail: "exit 1\n--- FAIL: TestThing",
	}

	plain := ansi.Strip(m.renderToolBlock(entry, 80))
	for _, want := range []string{"▲ bash  cwd scripts  timeout 30s", "$ go test ./...", "exit 1", "--- FAIL: TestThing"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("bash block lacks %q:\n%s", want, plain)
		}
	}
	if strings.Index(plain, "$ go test ./...") > strings.Index(plain, "exit 1") {
		t.Fatalf("the output is drawn before the command it came from:\n%s", plain)
	}
	// One pyramid per call, carrying the outcome: a second marker on the output
	// was the noise this replaced.
	if strings.Contains(plain, "▼") || strings.Contains(plain, "↙") {
		t.Fatalf("the output carries a pyramid of its own:\n%s", plain)
	}
}

// A failure is written in the failure colour, so a failed call reads as one
// without a marker saying so.
func TestToolBlockWritesAFailedResultInRed(t *testing.T) {
	// '#e67e80' and '#859289' as the truecolor sequences the formatter writes.
	const red, muted = "38;2;230;126;128", "38;2;133;146;137"

	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	failed := &block{
		role: "tool", toolKind: "call", toolName: string(contracts.BashTool),
		toolArgs: `{"command":"go test ./..."}`, toolStatus: string(contracts.ToolExecutionError),
		toolDetail: "exit 1\n--- FAIL: TestThing",
	}
	if line := rawToolLine(m.renderToolBlock(failed, 80), "FAIL"); !strings.Contains(line, red) {
		t.Fatalf("a failed result is not red: %q", line)
	}

	worked := &block{
		role: "tool", toolKind: "call", toolName: string(contracts.BashTool),
		toolArgs: `{"command":"go test ./..."}`, toolStatus: string(contracts.ToolExecutionSuccess),
		toolDetail: "exit 0\nok",
	}
	if line := rawToolLine(m.renderToolBlock(worked, 80), "ok"); !strings.Contains(line, muted) {
		t.Fatalf("a result that worked is not in the body colour: %q", line)
	}
}

// A call that has not answered yet shows the command alone: nothing follows it,
// and the pyramid says the call is still running by staying neutral.
func TestToolBlockShowsNothingAfterARunningCall(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	entry := &block{
		role: "tool", toolKind: "call", toolName: string(contracts.BashTool),
		toolArgs: `{"command":"sleep 60"}`,
	}
	plain := ansi.Strip(m.renderToolBlock(entry, 80))
	if !strings.Contains(plain, "$ sleep 60") {
		t.Fatalf("running bash call lacks its command:\n%s", plain)
	}
	if strings.Contains(plain, "exit ") {
		t.Fatalf("running bash call shows a result:\n%s", plain)
	}
}

// The command is bash, so it is lit by the bash lexer the approval pane uses. The
// output is whatever the command printed — not bash — so it stays plain.
func TestToolBlockHighlightsTheBashCommandOnly(t *testing.T) {
	// '#a7c080', the colour a bash string literal gets, and '#83c092', the one a
	// builtin gets, as the truecolor sequences the formatter writes.
	const stringColor, builtinColor = "38;2;167;192;128", "38;2;131;192;146"

	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	entry := &block{
		role: "tool", toolKind: "call", toolName: string(contracts.BashTool),
		toolArgs:   `{"command":"echo \"hi\" && ls -la"}`,
		toolStatus: string(contracts.ToolExecutionSuccess),
		toolDetail: "exit 0\ndone",
	}

	raw := m.renderToolBlock(entry, 80)
	script, output := rawToolLine(raw, "ls -la"), rawToolLine(raw, "done")
	if !strings.Contains(script, stringColor) || !strings.Contains(script, builtinColor) {
		t.Fatalf("the command is not lit by the bash lexer: %q", script)
	}
	if strings.Contains(output, stringColor) || strings.Contains(output, builtinColor) {
		t.Fatalf("the output is lit as bash: %q", output)
	}
}

// rawToolLine is the first rendered line containing needle, with its escapes
// intact: what a colour assertion needs.
func rawToolLine(rendered, needle string) string {
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// A result is folded under the call it answers, so what it contributes is a
// body beside the marker rather than a block of its own. The marker carries the
// status, so the body never repeats it.
func TestToolResultDetailShowsWhatTheToolDid(t *testing.T) {
	bashOutput := `{"exit_code":7,"output":"full output\n","truncated":true}`
	tests := []struct {
		name      string
		tool      contracts.ToolType
		status    contracts.ToolExecutionStatus
		output    string
		resultErr string
		want      string
	}{
		{
			name: "bash shows its exit code and output", tool: contracts.BashTool, status: contracts.ToolExecutionSuccess,
			output: bashOutput, want: "exit 7 · output truncated\nfull output",
		},
		{
			name: "bash without a structured result keeps its text", tool: contracts.BashTool, status: contracts.ToolExecutionSuccess,
			output: "legacy output", want: "legacy output",
		},
		{
			name: "a read that worked adds nothing", tool: contracts.ReadTool, status: contracts.ToolExecutionSuccess,
			output: "one\ntwo\n", want: "",
		},
		{
			name: "a search that worked adds nothing", tool: contracts.SearchTool, status: contracts.ToolExecutionSuccess,
			output: `[{"path":"main.go","line":3}]`, want: "",
		},
		{
			name: "an edit that worked adds nothing", tool: contracts.EditTool, status: contracts.ToolExecutionSuccess,
			output: `{"replacements":1}`, want: "",
		},
		{
			name: "a write that worked adds nothing", tool: contracts.WriteTool, status: contracts.ToolExecutionSuccess,
			output: `{"status":"written"}`, want: "",
		},
		{
			name: "an edit that failed says why", tool: contracts.EditTool, status: contracts.ToolExecutionError,
			resultErr: "old_string matched 2 times; use replace_all", want: "old_string matched 2 times; use replace_all",
		},
		{
			name: "a read that failed says why", tool: contracts.ReadTool, status: contracts.ToolExecutionError,
			resultErr: "permission denied", want: "permission denied",
		},
		{
			name: "a denied call says so", tool: contracts.BashTool, status: contracts.ToolExecutionDenied,
			resultErr: "tool execution was denied by session policy", want: "tool execution was denied by session policy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolResultDetail(string(tt.tool), string(tt.status), tt.output, tt.resultErr); got != tt.want {
				t.Fatalf("detail = %q, want %q", got, tt.want)
			}
		})
	}
}

// A command that exited nonzero failed, whatever the tool's own status was: the
// green pyramid has to mean the thing the reader ran worked.
func TestDisplayStatusMarksNonzeroBashExitAsFailure(t *testing.T) {
	if got := displayStatus(string(contracts.BashTool), string(contracts.ToolExecutionSuccess), `{"exit_code":1}`); got != string(contracts.ToolExecutionError) {
		t.Fatalf("status = %q, want %q", got, contracts.ToolExecutionError)
	}
	if got := displayStatus(string(contracts.BashTool), string(contracts.ToolExecutionSuccess), `{"exit_code":0}`); got != string(contracts.ToolExecutionSuccess) {
		t.Fatalf("status = %q, want %q", got, contracts.ToolExecutionSuccess)
	}
	if got := displayStatus(string(contracts.EditTool), string(contracts.ToolExecutionSuccess), `{"replacements":1}`); got != string(contracts.ToolExecutionSuccess) {
		t.Fatalf("status = %q, want %q", got, contracts.ToolExecutionSuccess)
	}
}

// A session that is live and the same session rebuilt from its timeline have to
// read alike: the result is folded into the call in both.
func TestLiveAndResumedBashResultsMatch(t *testing.T) {
	payload := `{"exit_code":7,"output":"full output\n","truncated":true}`
	call := contracts.ToolCall{ID: "call-1", Type: "function", Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"make test"}`,
	}}

	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventToolCall, ToolCall: &call})
	m.applySessionEvent(contracts.EventStream{Kind: contracts.EventToolResult, ToolResult: &contracts.ToolExecutionResult{
		Status: contracts.ToolExecutionSuccess, CallID: call.ID, CallName: contracts.BashTool, Output: payload,
	}})
	if len(m.blocks) != 1 {
		t.Fatalf("live blocks=%d want 1", len(m.blocks))
	}

	toolMessage := contracts.NewToolMessage(call.ID, payload, contracts.ToolExecutionSuccess)
	resumed := blocksFromRecords([]store.Record{
		{Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}}},
		{Kind: store.KindMessage, Message: &toolMessage},
	})
	if len(resumed) != 1 {
		t.Fatalf("resumed blocks=%d want 1", len(resumed))
	}

	if m.blocks[0].toolDetail != resumed[0].toolDetail {
		t.Fatalf("live detail %q != resumed detail %q", m.blocks[0].toolDetail, resumed[0].toolDetail)
	}
	if m.blocks[0].toolStatus != resumed[0].toolStatus {
		t.Fatalf("live status %q != resumed status %q", m.blocks[0].toolStatus, resumed[0].toolStatus)
	}
	if !strings.Contains(m.blocks[0].toolDetail, "exit 7") || !strings.Contains(m.blocks[0].toolDetail, "full output") {
		t.Fatalf("bash detail = %q, want the exit code and the output", m.blocks[0].toolDetail)
	}
	if m.blocks[0].toolStatus != string(contracts.ToolExecutionError) {
		t.Fatalf("status = %q, want a failed command to read as failed", m.blocks[0].toolStatus)
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

	for _, want := range []contracts.InfaiThinkingLevel{contracts.ThinkingOff, contracts.ThinkingLow, contracts.ThinkingHigh, contracts.ThinkingOff} {
		m.cycleThinking()
		if got := m.thinking; got != want {
			t.Fatalf("cycleThinking() = %q, want %q", got, want)
		}
	}
}

func TestSubmitKeepsDraftWhenNoSession(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("keep me")

	if cmd := m.submit(); cmd != nil {
		t.Fatal("submit dispatched without a session")
	}
	if got := m.composer.Value(); got != "keep me" {
		t.Fatalf("composer=%q want draft retained", got)
	}
}

func TestSubmitKeepsDraftAndImagesWhenModelLacksImage(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.session = store.SessionMeta{ID: uuid.New(), Model: "text-only"}
	m.modalities = []contracts.LLMSupportedModality{contracts.ModalityText}
	m.pending = []contracts.ImageInput{{Name: "a.png", MediaType: "image/png", Data: []byte("a")}}
	m.composer.SetValue("look")

	if cmd := m.submit(); cmd != nil {
		t.Fatal("submit dispatched with a text-only model")
	}
	if got := m.composer.Value(); got != "look" {
		t.Fatalf("composer=%q want draft retained", got)
	}
	if len(m.pending) != 1 {
		t.Fatalf("pending=%d want retained", len(m.pending))
	}
}

func TestModelSwitchClearsPendingAttachments(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.pending = []contracts.ImageInput{{Name: "a.png", MediaType: "image/png", Data: []byte("a")}}

	_, _ = m.Update(modelSetMsg{output: &glue.SessionOutput{SessionMeta: store.SessionMeta{ID: uuid.New(), Model: "m"}}})

	if m.pending != nil {
		t.Fatalf("pending survived model switch: %+v", m.pending)
	}
}

func TestSessionListCancelsObserverAndDropsQueuedEvents(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*chatModel) tea.Cmd
	}{
		{"slash", func(m *chatModel) tea.Cmd { return m.runCommand("/sessions") }},
		{"ctrl+o", func(m *chatModel) tea.Cmd {
			_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
			return cmd
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
			m.modal = nil
			m.session.ID = uuid.New()
			m.used = 19
			m.contextWindow = 100
			observerCtx, canceled := context.WithCancel(context.Background())
			m.sessionCancel = canceled
			defer canceled()
			oldID := m.sessionObserverID
			if cmd := tc.open(m); cmd == nil {
				t.Fatal("session list command missing")
			}
			if m.sessionCancel != nil || m.sessionStream != nil || m.sessionObserverID == oldID || observerCtx.Err() != context.Canceled {
				t.Fatal("old observer remained attached")
			}
			usage := `{"total_tokens":99}`
			_, _ = m.Update(sessionEventMsg{sessionID: m.session.ID, observerID: oldID, event: contracts.EventStream{Kind: contracts.NotifyAgentUsage, Content: &usage}})
			_, _ = m.Update(sessionJoinDoneMsg{sessionID: m.session.ID, observerID: oldID, err: fmt.Errorf("old join")})
			if m.used != 19 || len(m.blocks) != 0 {
				t.Fatalf("queued observer messages changed state: used=%d blocks=%d", m.used, len(m.blocks))
			}
		})
	}
}

func TestSessionLoadResetsStatusAndIgnoresOldObserver(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.modal = nil
	oldID := uuid.New()
	m.session = store.SessionMeta{ID: oldID, Provider: "old", Model: "old-model", Cwd: "/old"}
	m.used, m.contextWindow, m.thinking = 87, 100, contracts.ThinkingLow
	m.status, m.working, m.workStatus = contracts.SessionBusy, true, "old work"
	_ = m.startSessionObserver(oldID)
	oldObserver := m.sessionObserverID
	m.openSessionList()
	newID := uuid.New()
	_, _ = m.Update(sessionLoadedMsg{output: &glue.SessionOutput{
		SessionMeta:   store.SessionMeta{ID: newID, Provider: "new", Model: "new-model", Cwd: "/new"},
		ContextWindow: 200, Thinking: contracts.ThinkingHigh,
	}})
	status := ansi.Strip(m.statusView())
	for _, want := range []string{"new-model (new)", "/new", "thinking high", "0% 0/200"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q: %q", want, status)
		}
	}
	if strings.Contains(status, "old-model") || m.working || m.workStatus != "" {
		t.Fatalf("old turn survived load: status=%q work=%q", status, m.workStatus)
	}
	usage := `{"total_tokens":98}`
	_, _ = m.Update(sessionEventMsg{sessionID: oldID, observerID: oldObserver, event: contracts.EventStream{Kind: contracts.NotifyAgentUsage, Content: &usage}})
	if m.used != 0 {
		t.Fatal("old observer changed new session usage")
	}
}

func TestFailedSessionLoadResumesOldObserver(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.session.ID = uuid.New()
	m.modal = nil
	_ = m.startSessionObserver(m.session.ID)
	m.openSessionList()
	_, _ = m.Update(sessionLoadedMsg{err: fmt.Errorf("not found")})
	if m.modal == nil || m.sessionCancel != nil {
		t.Fatal("failed load should show notice while detached")
	}
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil || m.modal != nil || m.sessionCancel == nil {
		t.Fatal("acknowledgment did not resume old observer")
	}
}

func TestSessionLoadFetchesParentIdentity(t *testing.T) {
	parentID := uuid.New()
	client := &parentIdentityChatClient{parentID: parentID}

	msg, ok := loadSessionCmd(context.Background(), client, uuid.New())().(sessionLoadedMsg)
	if !ok {
		t.Fatal("load command did not return sessionLoadedMsg")
	}
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if client.requestedParent != parentID {
		t.Fatalf("requested parent = %s, want %s", client.requestedParent, parentID)
	}
	if msg.parentName != "Orchestrator" || msg.parentAgentKind != contracts.InteractiveAgent {
		t.Fatalf("parent identity = (%q, %q), want (Orchestrator, interactive)", msg.parentName, msg.parentAgentKind)
	}
}

func TestSessionLoadClearsPendingAttachments(t *testing.T) {
	m := newChatModel(context.Background(), stubChatClient{}, nil, RunOptions{})
	m.modal = nil
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.pending = []contracts.ImageInput{{Name: "a.png", MediaType: "image/png", Data: []byte("a")}}

	_, _ = m.Update(sessionLoadedMsg{output: &glue.SessionOutput{SessionMeta: store.SessionMeta{ID: uuid.New(), Model: "m"}}})

	if m.pending != nil {
		t.Fatalf("pending survived session load: %+v", m.pending)
	}
}

type navigationChatClient struct {
	stubChatClient
	opened uuid.UUID
}

func (c *navigationChatClient) LoadSession(_ context.Context, id uuid.UUID) (*glue.SessionOutput, error) {
	c.opened = id
	return &glue.SessionOutput{SessionMeta: store.SessionMeta{ID: id}}, nil
}

type parentIdentityChatClient struct {
	stubChatClient
	parentID        uuid.UUID
	requestedParent uuid.UUID
}

func (c *parentIdentityChatClient) LoadSession(context.Context, uuid.UUID) (*glue.SessionOutput, error) {
	return &glue.SessionOutput{SessionMeta: store.SessionMeta{ID: uuid.New(), ParentID: c.parentID}}, nil
}

func (c *parentIdentityChatClient) GetSessionIdentity(_ context.Context, id uuid.UUID) (string, contracts.AgentKind, error) {
	c.requestedParent = id
	return "Orchestrator", contracts.InteractiveAgent, nil
}

type stubChatClient struct{}

func (stubChatClient) Chat(context.Context, contracts.UserInput, contracts.InfaiThinkingLevel, func(contracts.EventStreamKind, string), func(ApprovalUpdate)) (*ChatReply, error) {
	return &ChatReply{}, nil
}
func (stubChatClient) SendMessage(context.Context, contracts.UserInput, contracts.InfaiThinkingLevel) error {
	return nil
}
func (stubChatClient) JoinSession(context.Context, uuid.UUID, func(glue.SessionView), func(contracts.EventStream)) error {
	return nil
}
func (stubChatClient) ResolveApproval(context.Context, Approval, string, string) error { return nil }
func (stubChatClient) CancelTurn(context.Context, uuid.UUID) error                     { return nil }
func (stubChatClient) SetSession(uuid.UUID)                                            {}
func (stubChatClient) CreateSession(context.Context, SessionCreateOptions) (*glue.SessionOutput, error) {
	return &glue.SessionOutput{}, nil
}
func (stubChatClient) LoadSession(context.Context, uuid.UUID) (*glue.SessionOutput, error) {
	return &glue.SessionOutput{}, nil
}
func (stubChatClient) GetSession(context.Context, uuid.UUID) (*store.SessionMeta, []store.Record, error) {
	return nil, nil, nil
}
func (stubChatClient) GetSessionIdentity(context.Context, uuid.UUID) (string, contracts.AgentKind, error) {
	return "", "", nil
}
func (stubChatClient) DeleteSession(context.Context, uuid.UUID) error { return nil }
func (stubChatClient) CloseSession(context.Context, uuid.UUID) error  { return nil }
func (stubChatClient) RenameSession(context.Context, uuid.UUID, string) (*store.SessionMeta, error) {
	return &store.SessionMeta{}, nil
}
func (stubChatClient) ListSessions(context.Context) ([]contracts.SessionSummary, error) {
	return nil, nil
}
func (stubChatClient) ListAllProviderModels(context.Context) ([]glue.ListModelOutput, error) {
	return nil, nil
}
func (stubChatClient) SetSessionModel(context.Context, string, string) (*glue.SessionOutput, error) {
	return &glue.SessionOutput{}, nil
}
func (stubChatClient) Compact(context.Context) (*store.SessionMeta, error) {
	return &store.SessionMeta{}, nil
}
func (stubChatClient) GetTimeline(context.Context, uuid.UUID) (*TimelineView, error) {
	return &TimelineView{}, nil
}
func (stubChatClient) SelectBranch(context.Context, uuid.UUID, uuid.UUID) (contracts.TaskChecklistState, error) {
	return contracts.TaskChecklistState{}, nil
}

func TestCtrlUClearsImagesAndComposer(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.pending = []contracts.ImageInput{{Name: "a.png", MediaType: "image/png", Data: []byte("a")}}
	m.composer.SetValue("draft")

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))

	if m.pending != nil {
		t.Fatalf("pending=%+v want cleared", m.pending)
	}
	if got := m.composer.Value(); got != "" {
		t.Fatalf("composer=%q want cleared", got)
	}
}
