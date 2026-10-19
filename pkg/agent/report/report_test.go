package report

import (
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

func TestRenderLinksToolResultsToCalls(t *testing.T) {
	call := contracts.NewAssistantMessage("")
	call.ToolCalls = []contracts.ToolCall{{ID: "call-1", Type: "function", Function: contracts.Function{Name: contracts.ToolType("bash"), Arguments: `{"command":"ls"}`}}}
	callEvent := messageEvent(uuid.Nil, call)
	resultEvent := messageEvent(callEvent.ID, contracts.NewToolMessage("call-1", "file.txt", contracts.ToolExecutionStatus("success")))
	out := render(t, []store.Event{callEvent, resultEvent}, resultEvent.ID)
	for _, want := range []string{"tool result · bash", "answers call", `href="#s0-call-`, "call-1", "&#34;command&#34;: &#34;ls&#34;", "file.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q", want)
		}
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
