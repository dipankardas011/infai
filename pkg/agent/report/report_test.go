package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

func messageEvent(parent uuid.UUID, message contracts.ChatMessage) store.Event {
	return store.Event{
		ID:       uuid.New(),
		ParentID: parent,
		Kind:     store.KindMessage,
		Record:   &store.Record{Kind: store.KindMessage, Timestamp: time.Unix(1700000000, 0), Message: &message},
	}
}

func render(t *testing.T, events []store.Event, head uuid.UUID) string {
	t.Helper()
	page, err := Render(store.SessionMeta{ID: uuid.New(), Name: "demo"}, events, head)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(page)
}

func TestRenderCompatibilityShowsEveryBranch(t *testing.T) {
	root := messageEvent(uuid.Nil, contracts.NewUserMessage("question"))
	active := messageEvent(root.ID, contracts.NewAssistantMessage("kept answer"))
	abandoned := messageEvent(root.ID, contracts.NewAssistantMessage("abandoned answer"))
	abandoned.BranchFrom = &root.ID

	out := render(t, []store.Event{root, active, abandoned}, active.ID)
	for _, event := range []store.Event{root, active, abandoned} {
		if !strings.Contains(out, `id="`+eventDOMID(0, event.ID)+`"`) {
			t.Errorf("event %s missing from the report", event.ID)
		}
	}
	for _, want := range []string{"kept answer", "abandoned answer", "fork of " + short(root.ID), `class="badge head"`} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if got := strings.Count(out, `<span class="badge off">off branch</span>`); got != 1 {
		t.Errorf("off-branch badges = %d, want 1", got)
	}
}

func TestRenderSessionsEmbedsSwitcherAndRootStartsActive(t *testing.T) {
	rootEvent := messageEvent(uuid.Nil, contracts.NewUserMessage("root work"))
	childEvent := messageEvent(uuid.Nil, contracts.NewAssistantMessage("offspring work"))
	rootID, childID := uuid.New(), uuid.New()
	outBytes, err := RenderSessions([]SessionTimeline{
		{Meta: store.SessionMeta{ID: rootID, Name: "root session", AgentKind: contracts.InteractiveAgent}, Events: []store.Event{rootEvent}, Head: rootEvent.ID},
		{Meta: store.SessionMeta{ID: childID, ParentID: rootID, Name: "worker", AgentKind: contracts.AgentKind("subagent")}, Events: []store.Event{childEvent}, Head: childEvent.ID},
	})
	if err != nil {
		t.Fatalf("RenderSessions: %v", err)
	}
	out := string(outBytes)
	for _, want := range []string{`data-session-target="session-0"`, `data-session-target="session-1"`, "root session", "worker", "root ·", "offspring ·", `id="session-0"`, `id="session-1"`, "selectSession"} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-session report missing %q", want)
		}
	}
	if !strings.Contains(out, `class="session-panel active" id="session-0"`) {
		t.Error("root session must be visible on launch")
	}
	if strings.Contains(out, `class="session-panel active" id="session-1"`) {
		t.Error("offspring session must not be visible on launch")
	}
}

func TestTimelineUsesTheTUITreeLayout(t *testing.T) {
	root := messageEvent(uuid.Nil, contracts.NewUserMessage("root"))
	left := messageEvent(root.ID, contracts.NewAssistantMessage("left"))
	right := messageEvent(root.ID, contracts.NewAssistantMessage("right"))
	right.BranchFrom = &root.ID
	leaf := messageEvent(left.ID, contracts.NewUserMessage("leaf"))

	out := render(t, []store.Event{root, left, right, leaf}, leaf.ID)
	for _, want := range []string{
		`class="tl-line"`, `class="tl-node"`, `class="tl-guide"`, `class="tl-elbow"`,
		`tl-role user`, `tl-role assistant`, `tl-cursor">*`, `tl-row assistant off fork`,
		`data-parent-id="` + root.ID.String() + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("branch timeline missing %q", want)
		}
	}
	for _, event := range []store.Event{root, left, right, leaf} {
		if !strings.Contains(out, `id="`+eventDOMID(0, event.ID)+`"`) {
			t.Errorf("event %s missing from the timeline", event.ID)
		}
	}
}

func TestRenderMarkdownAndRejectsRawHTML(t *testing.T) {
	text := "# Heading\n\n- one\n- two\n\n```go\nfmt.Println(\"ok\")\n```\n\n[safe](https://example.com)\n\n<script>alert('x')</script>"
	event := messageEvent(uuid.Nil, contracts.NewAssistantMessage(text))
	out := render(t, []store.Event{event}, event.ID)

	for _, want := range []string{"<h1>Heading</h1>", "<ul>", "<pre><code>", `<a href="https://example.com"`} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered markdown missing %q", want)
		}
	}
	if strings.Contains(out, "<script>alert('x')</script>") {
		t.Error("raw markdown HTML became executable")
	}
	if !strings.Contains(out, `\u003cscript\u003ealert`) {
		t.Error("raw JSON should retain escaped source text")
	}
}

func TestUsageProgressShowsTokenDetailsAndCapsAtContextWindow(t *testing.T) {
	event := messageEvent(uuid.Nil, contracts.NewAssistantMessage("answer"))
	meta := store.SessionMeta{ID: uuid.New(), Name: "usage", LastTurnMeta: &store.LastTurnMeta{
		Usage: contracts.TokenUsage{
			PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500,
			PromptTokensDetails:     &contracts.PromptTokensDetails{CachedTokens: 400, CacheWriteTokens: 50},
			CompletionTokensDetails: &contracts.CompletionTokensDetails{ReasoningTokens: 125},
		},
		ContextWindow: 1000,
	}}
	page, err := Render(meta, []store.Event{event}, event.ID)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := string(page)
	for _, want := range []string{"1200", "300", "1500", "1000", "cached 400", "cache write 50", "reasoning 125", "exceeds window", `data-feature="usage-progress"`, `<div class="usage-progress-bar prompt-context-progress" role="progressbar"`, `style="width:100.00%"`, `<progress class="usage-progress sr-only" max="100" value="100.00"`} {
		if !strings.Contains(out, want) {
			t.Errorf("usage display missing %q", want)
		}
	}
	if strings.Contains(out, `"prompt_tokens"`) {
		t.Error("last-turn usage must not be displayed as JSON metadata")
	}

	meta.LastTurnMeta.ContextWindow = 0
	page, err = Render(meta, []store.Event{event}, event.ID)
	if err != nil {
		t.Fatalf("Render with zero context: %v", err)
	}
	if strings.Contains(string(page), "NaN") || strings.Contains(string(page), "+Inf") {
		t.Error("zero context window produced an invalid percentage")
	}
}

func TestRenderUsageProgressExistsWithoutLastTurnMetadata(t *testing.T) {
	event := messageEvent(uuid.Nil, contracts.NewAssistantMessage("answer"))
	out := render(t, []store.Event{event}, event.ID)
	for _, want := range []string{`data-feature="usage-progress"`, "No last-turn token usage was recorded", `<div class="usage-progress-bar prompt-context-progress" role="progressbar"`, `style="width:0.00%"`, `<progress class="usage-progress sr-only" max="100" value="0.00"`} {
		if !strings.Contains(out, want) {
			t.Errorf("empty usage progress missing %q", want)
		}
	}
}

func TestRenderKeepsFullBlobBackedPayload(t *testing.T) {
	huge := strings.Repeat("x", 5000) + "TAIL-MARKER"
	event := messageEvent(uuid.Nil, contracts.NewAssistantMessage(huge))
	event.BlobHash = "deadbeef"
	event.Preview = &store.EventPreview{Role: "assistant", Text: "xxx…"}
	out := render(t, []store.Event{event}, event.ID)
	if got := strings.Count(out, "TAIL-MARKER"); got < 2 {
		t.Errorf("tail marker appears %d times, want body and raw payload", got)
	}
	if !strings.Contains(out, "blob deadbeef") || strings.Contains(out, "payload unavailable") {
		t.Error("resolved blob-backed event metadata is incorrect")
	}
}

func TestRenderFallsBackToPreviewWhenUnresolved(t *testing.T) {
	event := store.Event{ID: uuid.New(), Kind: store.KindMessage, BlobHash: "cafe", Preview: &store.EventPreview{Role: "user", Text: "preview text"}}
	out := render(t, []store.Event{event}, event.ID)
	if !strings.Contains(out, "payload unavailable") || !strings.Contains(out, "preview text") {
		t.Error("unresolved event must show its preview")
	}
}

func TestRenderOnlyEmbedsSafeImageTypesAndAddsLightbox(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G'}
	good := contracts.NewUserMessage("pic")
	good.Images = []contracts.ImageInput{{Name: "ok.png", MediaType: "image/png", Data: data}}
	hostile := contracts.NewUserMessage("pic")
	hostile.Images = []contracts.ImageInput{{Name: "bad", MediaType: `image/png" onerror="alert(1)`, Data: data}}
	svg := contracts.NewUserMessage("pic")
	svg.Images = []contracts.ImageInput{{Name: "svg", MediaType: "image/svg+xml", Data: data}}

	out := render(t, []store.Event{messageEvent(uuid.Nil, good)}, uuid.Nil)
	for _, want := range []string{`<img src="data:image/png;base64,`, "data-lightbox", `class="lightbox"`, "e.key===\u0027Escape\u0027"} {
		if !strings.Contains(out, want) {
			t.Errorf("safe inline image UX missing %q", want)
		}
	}
	for name, message := range map[string]contracts.ChatMessage{"hostile": hostile, "svg": svg} {
		event := messageEvent(uuid.Nil, message)
		out := render(t, []store.Event{event}, event.ID)
		if strings.Contains(out, `<img src="data:`) {
			t.Errorf("%s media type produced an inline image", name)
		}
	}
}

func TestToolCallAndResultShareOneRow(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "call-1", Type: "function", Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"ls"}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	resultEvent := messageEvent(callEvent.ID, contracts.NewToolMessage("call-1", "file.txt", contracts.ToolExecutionSuccess))
	out := render(t, []store.Event{callEvent, resultEvent}, resultEvent.ID)

	// One row for the call, none for the result it answers.
	if got := strings.Count(out, `class="tl-row`); got != 1 {
		t.Errorf("timeline rows = %d, want the call and its result to share one", got)
	}
	if strings.Contains(out, `id="`+eventDOMID(0, resultEvent.ID)+`"`) {
		t.Error("the merged result still has its own row")
	}
	// The row carries both the request and the result, with an outcome badge.
	callRow := out[strings.Index(out, `id="`+eventDOMID(0, callEvent.ID)+`"`):]
	for _, want := range []string{"⚙ bash", `class="badge ok">ok`, "ls", "file.txt", "call-1"} {
		if !strings.Contains(callRow, want) {
			t.Errorf("merged call row missing %q", want)
		}
	}
	// The head marker follows the result to its call.
	if !strings.Contains(callRow, `class="badge head"`) {
		t.Error("the head badge must stay visible on the call")
	}
}

func TestMergedResultsKeepTheTimelineGraph(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{
		{ID: "c1", Function: contracts.Function{Name: contracts.ReadTool, Arguments: `{"path":"a"}`}},
		{ID: "c2", Function: contracts.Function{Name: contracts.ReadTool, Arguments: `{"path":"b"}`}},
	}
	first := messageEvent(uuid.Nil, contracts.NewUserMessage("go"))
	callEvent := messageEvent(first.ID, call)
	r1 := messageEvent(callEvent.ID, contracts.NewToolMessage("c1", "a", contracts.ToolExecutionSuccess))
	r2 := messageEvent(r1.ID, contracts.NewToolMessage("c2", "b", contracts.ToolExecutionSuccess))
	answer := messageEvent(r2.ID, contracts.NewAssistantMessage("done"))

	out := render(t, []store.Event{first, callEvent, r1, r2, answer}, answer.ID)
	if got := strings.Count(out, `class="tl-row`); got != 3 {
		t.Errorf("timeline rows = %d, want user, call and answer", got)
	}
	// The answer is re-parented to the call, not to a hidden result.
	answerRow := out[strings.Index(out, `id="`+eventDOMID(0, answer.ID)+`"`):]
	answerRow = answerRow[:strings.Index(answerRow, `>`)]
	if !strings.Contains(answerRow, `data-parent-id="`+callEvent.ID.String()+`"`) {
		t.Errorf("answer parent = %s, want the call %s", answerRow, callEvent.ID)
	}
}

func TestUsageProgressMeasuresTheWindowNotTheCumulativeTurn(t *testing.T) {
	event := messageEvent(uuid.Nil, contracts.NewAssistantMessage("answer"))
	meta := store.SessionMeta{ID: uuid.New(), Name: "usage", LastTurnMeta: &store.LastTurnMeta{
		Usage: contracts.TokenUsage{
			PromptTokens: 60000, CompletionTokens: 5000, TotalTokens: 65000, ContextTokens: 32000,
		},
		ContextWindow: 100000,
	}}
	page, err := Render(meta, []store.Event{event}, event.ID)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := string(page)
	for _, want := range []string{"32000 used / 100000 context", `style="width:32.00%"`, "60000", "65000"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage display missing %q", want)
		}
	}
	if strings.Contains(out, "exceeds window") {
		t.Error("the cumulative turn was used as the window measurement")
	}
}

func TestRenderEvalResultShowsBadgeAndOutput(t *testing.T) {
	// Passing eval
	passMsg := contracts.NewAssistantMessage("done")
	passMsg.Eval = &contracts.EvalResult{Passed: true, Attempt: 1}
	passEvent := messageEvent(uuid.Nil, passMsg)
	out := render(t, []store.Event{passEvent}, passEvent.ID)
	for _, want := range []string{`class="eval pass"`, "✓ PASS", "attempt 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("passing eval missing %q", want)
		}
	}

	// Failing eval with output
	failMsg := contracts.NewAssistantMessage("tried")
	failMsg.Eval = &contracts.EvalResult{Passed: false, Attempt: 2, Output: "test xyz failed"}
	failEvent := messageEvent(uuid.Nil, failMsg)
	out = render(t, []store.Event{failEvent}, failEvent.ID)
	for _, want := range []string{`class="eval fail"`, "✗ FAIL", "attempt 2", "test xyz failed", `class="eval-output"`} {
		if !strings.Contains(out, want) {
			t.Errorf("failing eval missing %q", want)
		}
	}
}

func TestRenderTaskChecklistFromToolResult(t *testing.T) {
	state := contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{
		{Title: "Write tests", Description: "Unit tests for report", Status: contracts.TaskCompleted},
		{Title: "Update docs", Description: "Add checklist docs", Status: contracts.TaskInProgress},
		{Title: "Review PR", Description: "Final review", Status: contracts.TaskPending},
	}}
	stateJSON, _ := json.Marshal(state)

	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "cl-1", Type: "function", Function: contracts.Function{Name: contracts.TaskChecklistTool, Arguments: `{"action":"list"}`}}}
	callEvent := messageEvent(uuid.Nil, call)
	resultEvent := messageEvent(callEvent.ID, contracts.NewToolMessage("cl-1", string(stateJSON), contracts.ToolExecutionSuccess))

	out := render(t, []store.Event{callEvent, resultEvent}, resultEvent.ID)
	for _, want := range []string{
		`class="checklist"`, `class="checklist-title"`,
		"Write tests", "Unit tests for report", `class="checklist-item completed"`,
		"Update docs", "Add checklist docs", `class="checklist-item in_progress"`,
		"Review PR", "Final review", `class="checklist-item pending"`,
		"✓", "▸", "○",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task checklist from tool result missing %q", want)
		}
	}
	// The structured checklist is the readable form; the raw result is still
	// shown in the expanded cell like every other tool result.
	if !hasToolOutput(out, "&#34;items&#34;") {
		t.Error("the checklist result output is missing from the expanded cell")
	}
}

func TestRenderTaskChecklistFromCompaction(t *testing.T) {
	state := contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{
		{Title: "Compacted task", Description: "Survived compaction", Status: contracts.TaskCompleted},
	}}
	compactionEvent := store.Event{
		ID:   uuid.New(),
		Kind: store.KindCompaction,
		Record: &store.Record{
			Kind:      store.KindCompaction,
			Timestamp: time.Unix(1700000000, 0),
			Compaction: &store.CompactionRecord{
				Summary:       "compacted context",
				TaskChecklist: &state,
			},
		},
	}
	out := render(t, []store.Event{compactionEvent}, compactionEvent.ID)
	for _, want := range []string{
		"Compacted task", "Survived compaction", `class="checklist-item completed"`,
		"task checklist · raw JSON",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compaction checklist missing %q", want)
		}
	}
}

func TestRenderSessionLevelTaskChecklist(t *testing.T) {
	state := contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{
		{Title: "Session task", Description: "Visible in overview", Status: contracts.TaskInProgress},
	}}
	stateJSON, _ := json.Marshal(state)

	user := messageEvent(uuid.Nil, contracts.NewUserMessage("do work"))
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "cl-s", Type: "function", Function: contracts.Function{Name: contracts.TaskChecklistTool, Arguments: `{"action":"add","title":"Session task","description":"Visible in overview"}`}}}
	callEvent := messageEvent(user.ID, call)
	resultEvent := messageEvent(callEvent.ID, contracts.NewToolMessage("cl-s", string(stateJSON), contracts.ToolExecutionSuccess))
	answer := messageEvent(resultEvent.ID, contracts.NewAssistantMessage("working on it"))

	outBytes, err := Render(store.SessionMeta{ID: uuid.New(), Name: "checklist session"}, []store.Event{user, callEvent, resultEvent, answer}, answer.ID)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := string(outBytes)
	for _, want := range []string{
		`data-feature="task-checklist"`, "last known state",
		"Session task", "Visible in overview",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("session-level task checklist missing %q", want)
		}
	}
}

func TestToolCallSummaryUsesTheIdentifyingArgument(t *testing.T) {
	readCall := contracts.NewAssistantMessage("")
	readCall.ToolCalls = []contracts.ToolCall{{ID: "r1", Function: contracts.Function{
		Name: contracts.ReadTool, Arguments: `{"path":"pkg/agent/report/template.go","limit":100}`,
	}}}
	searchCall := contracts.NewAssistantMessage("")
	searchCall.ToolCalls = []contracts.ToolCall{{ID: "s1", Function: contracts.Function{
		Name: contracts.SearchTool, Arguments: `{"path":"pkg","pattern":"template.Must","include":["*.go"]}`,
	}}}

	out := render(t, []store.Event{messageEvent(uuid.Nil, readCall), messageEvent(uuid.Nil, searchCall)}, uuid.Nil)
	for _, want := range []string{"read path=pkg/agent/report/template.go", "search pattern=template.Must"} {
		if !strings.Contains(out, want) {
			t.Errorf("call summary missing %q", want)
		}
	}
	for _, unwanted := range []string{"read limit=100", "search path=pkg"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("call summary identified by a knob: %q", unwanted)
		}
	}
}

func TestToolResultsKeepPayloadOutOfTheSummaryButShowItWhenExpanded(t *testing.T) {
	source := "# Heading\n\npackage report\n\nvar Page = `{{define \"styles\"}}`\n"
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "r1", Function: contracts.Function{
		Name: contracts.ReadTool, Arguments: `{"path":"template.go"}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	result := messageEvent(callEvent.ID, contracts.NewToolMessage("r1", source, contracts.ToolExecutionSuccess))
	out := render(t, []store.Event{callEvent, result}, result.ID)

	summary := rowSummary(t, out, callEvent)
	if !strings.Contains(summary, "tool_call:") || !strings.Contains(summary, `class="badge ok">ok`) {
		t.Errorf("call summary = %q, want the call line and an ok badge", summary)
	}
	for _, leaked := range []string{"var Page =", "package report", "Heading", "ok · "} {
		if strings.Contains(summary, leaked) {
			t.Errorf("read result leaked %q into the collapsed summary", leaked)
		}
	}

	if !hasToolOutput(out, "# Heading") {
		t.Error("the expanded cell must show the read output preformatted")
	}
}

// toolOutputs returns the body of every tool output block, so a test can tell
// what the expanded cell shows apart from the raw JSON payload.
func toolOutputs(out string) []string {
	const open, close = `<pre class="tool-output">`, `</pre>`
	var bodies []string
	for {
		i := strings.Index(out, open)
		if i < 0 {
			return bodies
		}
		out = out[i+len(open):]
		j := strings.Index(out, close)
		if j < 0 {
			return bodies
		}
		bodies = append(bodies, out[:j])
		out = out[j+len(close):]
	}
}

func hasToolOutput(out, want string) bool {
	for _, body := range toolOutputs(out) {
		if strings.Contains(body, want) {
			return true
		}
	}
	return false
}

// rowSummary returns the collapsed line of one event, so a test can assert what
// a reader sees before expanding it.
func rowSummary(t *testing.T, out string, event store.Event) string {
	t.Helper()
	start := strings.Index(out, `id="`+eventDOMID(0, event.ID)+`"`)
	if start < 0 {
		t.Fatalf("event %s missing from the report", event.ID)
	}
	open := strings.Index(out[start:], `<summary class="tl-summary">`)
	if open < 0 {
		t.Fatalf("event %s has no summary", event.ID)
	}
	open += start + len(`<summary class="tl-summary">`)
	end := strings.Index(out[open:], `</summary>`)
	if end < 0 {
		t.Fatalf("event %s summary is unterminated", event.ID)
	}
	return out[open : open+end]
}

func TestFailedToolResultShowsTheReason(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "r1", Function: contracts.Function{
		Name: contracts.ReadTool, Arguments: `{"path":"huge.bin"}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	result := messageEvent(callEvent.ID, contracts.NewToolMessage("r1", "the file exceeds the read size limit", contracts.ToolExecutionError))
	out := render(t, []store.Event{callEvent, result}, result.ID)
	if summary := rowSummary(t, out, callEvent); !strings.Contains(summary, `class="badge err">err`) {
		t.Errorf("call summary = %q, want an err badge", summary)
	}
	if !hasToolOutput(out, "the file exceeds the read size limit") {
		t.Error("a failed result must show its reason in the expanded cell")
	}
}

func TestBashResultShowsExitAndFirstLine(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "b1", Function: contracts.Function{
		Name: contracts.BashTool, Arguments: `{"command":"go test ./..."}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	result := messageEvent(callEvent.ID, contracts.NewToolMessage("b1", `{"exit_code":1,"output":"--- FAIL: TestX\nmore"}`, contracts.ToolExecutionSuccess))
	out := render(t, []store.Event{callEvent, result}, result.ID)
	if summary := rowSummary(t, out, callEvent); !strings.Contains(summary, `class="badge ok">ok`) {
		t.Errorf("call summary = %q, want an ok badge", summary)
	}
	if !hasToolOutput(out, "--- FAIL: TestX") {
		t.Error("bash output is missing from the expanded cell")
	}
	if !strings.Contains(out, "bash: go test ./...") || !strings.Contains(out, ">exit 1<") {
		t.Error("the bash call line or its exit badge is missing")
	}
}

func TestEveryToolResultShowsItsOutputWhenExpanded(t *testing.T) {
	cases := []struct{ name, output, marker string }{
		{string(contracts.SearchTool), "pkg/x.go:1: hit\npkg/y.go:2: hit\n", "pkg/x.go:1: hit"},
		{string(contracts.ListTool), `[{"name":"nav.tmpl","size":10}]`, "nav.tmpl"},
		{string(contracts.WriteTool), `{"replacements":1}`, "replacements"},
		{"mcp_echo", `{"value":"hi"}`, "hi"},
	}
	for _, tc := range cases {
		call := contracts.NewAssistantMessage("")
		call.ToolCalls = []contracts.ToolCall{{ID: "t1", Function: contracts.Function{
			Name: contracts.ToolType(tc.name), Arguments: `{"path":"."}`,
		}}}
		callEvent := messageEvent(uuid.Nil, call)
		result := messageEvent(callEvent.ID, contracts.NewToolMessage("t1", tc.output, contracts.ToolExecutionSuccess))
		out := render(t, []store.Event{callEvent, result}, result.ID)

		if summary := rowSummary(t, out, callEvent); strings.Contains(summary, tc.marker) {
			t.Errorf("%s leaked its output into the collapsed summary", tc.name)
		}
		if !hasToolOutput(out, tc.marker) {
			t.Errorf("%s output is missing from the expanded cell", tc.name)
		}
	}
}

func TestToolOutputIsEscaped(t *testing.T) {
	payload := "</pre><script>alert(1)</script><img src=x onerror=alert(2)>"
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "r1", Function: contracts.Function{
		Name: contracts.ReadTool, Arguments: `{"path":"evil"}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	result := messageEvent(callEvent.ID, contracts.NewToolMessage("r1", payload, contracts.ToolExecutionSuccess))
	out := render(t, []store.Event{callEvent, result}, result.ID)

	if strings.Contains(out, "<script>alert(1)</script>") || strings.Contains(out, "<img src=x onerror=") {
		t.Fatal("tool output was emitted as live HTML")
	}
	if !hasToolOutput(out, "&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("tool output is not shown escaped in the expanded cell")
	}
}

func TestTaskChecklistCallShowsItsAction(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "c1", Function: contracts.Function{
		Name: contracts.TaskChecklistTool, Arguments: `{"action":"set_status","title":"Beta","status":"completed"}`,
	}}}
	out := render(t, []store.Event{messageEvent(uuid.Nil, call)}, uuid.Nil)
	if !strings.Contains(out, "task_checklist action=set_status") {
		t.Error("a checklist call must be identified by its action, not its title")
	}
}

func TestUnresolvedToolPreviewUsesTheResultRole(t *testing.T) {
	event := store.Event{ID: uuid.New(), Kind: store.KindMessage, BlobHash: "cafe", Preview: &store.EventPreview{Role: "tool", Text: "exit 0"}}
	out := render(t, []store.Event{event}, event.ID)
	if !strings.Contains(out, `class="tl-role tool_result"`) {
		t.Error("an unresolved tool preview must render as a tool result line")
	}
	if strings.Contains(out, `class="tl-role tool"`) {
		t.Error("the wire role leaked into the timeline")
	}
}

func TestBranchFromAMergedResultLandsOnTheCall(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "c1", Function: contracts.Function{
		Name: contracts.ReadTool, Arguments: `{"path":"a"}`,
	}}}
	callEvent := messageEvent(uuid.Nil, call)
	result := messageEvent(callEvent.ID, contracts.NewToolMessage("c1", "a", contracts.ToolExecutionSuccess))
	branch := messageEvent(result.ID, contracts.NewAssistantMessage("branch answer"))
	branch.BranchFrom = &result.ID

	out := render(t, []store.Event{callEvent, result, branch}, branch.ID)
	if got := strings.Count(out, `class="tl-row`); got != 2 {
		t.Errorf("timeline rows = %d, want the call and its branch", got)
	}
	if !strings.Contains(out, "fork of "+short(callEvent.ID)) {
		t.Error("the fork must be attributed to the visible call, not the merged result")
	}
	branchRow := out[strings.Index(out, `id="`+eventDOMID(0, branch.ID)+`"`):]
	branchRow = branchRow[:strings.Index(branchRow, `>`)]
	if !strings.Contains(branchRow, `data-parent-id="`+callEvent.ID.String()+`"`) {
		t.Errorf("branch parent = %s, want the call", branchRow)
	}
}
