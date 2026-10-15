package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// statsTestModel is a chat with a session whose last turn reported an
// accounting: 32.1k prompt of which 28.4k was cached, and 5.4k completion of
// which 3.1k was reasoning, against a 1M window.
func statsTestModel() *chatModel {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width, m.height = 100, 30
	m.session = store.SessionMeta{
		ID: uuid.New(), Name: "Deploy the cluster", Provider: "deepseek",
		Model: "deepseek-flash", Cwd: "/ws/infai", AgentKind: contracts.InteractiveAgent,
	}
	m.status = contracts.SessionIdle
	m.contextWindow = 1_000_000
	m.used = 37_500
	m.lastUsage = &contracts.TokenUsage{
		PromptTokens:            32_100,
		CompletionTokens:        5_400,
		TotalTokens:             37_500,
		PromptTokensDetails:     &contracts.PromptTokensDetails{CachedTokens: 28_400},
		CompletionTokensDetails: &contracts.CompletionTokensDetails{ReasoningTokens: 3_100},
	}
	return m
}

// The stats page answers the two questions a session's numbers are read for:
// how full the window is, and how the last turn spent what it did. Ctrl+D opens
// it from the chat, and the key that opened it closes it again.
func TestStatsPageShowsContextAndTheLastTurn(t *testing.T) {
	m := statsTestModel()

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if m.modal == nil || m.modal.kind != modalStats {
		t.Fatal("ctrl+d did not open the session stats page")
	}
	page := ansi.Strip(renderSelectionScreen(m.modal, m.width, m.height, m.styles))
	for _, want := range []string{
		"Deploy the cluster", "deepseek-flash (deepseek)", "/ws/infai",
		"37.5k of 1M", "3%",
		"LAST TURN  37.5k tokens",
		"cached 28.4k (88%)", "new 3.7k",
		"reasoning 3.1k (57%)", "text 2.3k",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("stats page lacks %q:\n%s", want, page)
		}
	}
	if lines := strings.Split(page, "\n"); len(lines) > m.height {
		t.Fatalf("stats page is %d rows on a %d-row terminal", len(lines), m.height)
	}

	// The composer is not behind the page: ctrl+d closes it, as esc would.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if m.modal != nil {
		t.Fatal("ctrl+d did not close the page it opened")
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.modal != nil {
		t.Fatal("esc did not close the page")
	}
}

// A session resumed without a live report still shows the turn that filled its
// context: the report the session saved is what the page falls back to.
func TestStatsPageFallsBackToTheSavedTurn(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width, m.height = 100, 30
	m.session = store.SessionMeta{
		ID: uuid.New(), Name: "Deploy the cluster", AgentKind: contracts.InteractiveAgent,
		LastTurnMeta: &store.LastTurnMeta{
			Usage: contracts.TokenUsage{
				PromptTokens:        32_100,
				CompletionTokens:    5_400,
				PromptTokensDetails: &contracts.PromptTokensDetails{CachedTokens: 28_400},
			},
			ContextWindow: 1_000_000,
			Provider:      "deepseek",
			Model:         "deepseek-flash",
		},
	}
	m.status = contracts.SessionIdle

	stats := m.sessionStats()
	if stats.used != 37_500 {
		t.Fatalf("context used=%d want the 37500 the saved turn measured", stats.used)
	}
	if stats.contextWindow != 1_000_000 {
		t.Fatalf("window=%d want the 1000000 the saved turn recorded", stats.contextWindow)
	}
	page := ansi.Strip(renderStatsPage(m))
	for _, want := range []string{"37.5k of 1M", "cached 28.4k (88%)"} {
		if !strings.Contains(page, want) {
			t.Fatalf("resumed stats page lacks %q:\n%s", want, page)
		}
	}

	// A session that has never reported a turn says so instead of drawing zero
	// as if it were a measurement. Its model's window is still known, because
	// the session was opened with it.
	m.session.LastTurnMeta = nil
	m.contextWindow = 1_000_000
	page = ansi.Strip(renderStatsPage(m))
	for _, want := range []string{"CONTEXT", "0 of 1M", "nothing measured yet", "no turn has reported tokens in this session yet"} {
		if !strings.Contains(page, want) {
			t.Fatalf("unreported stats page lacks %q:\n%s", want, page)
		}
	}
}

// A window that is not known is said to be unknown: the page has one bar, and
// drawing it empty would read as an empty context.
func TestStatsPageSaysWhenTheWindowIsUnknown(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.width, m.height = 100, 30
	m.session = store.SessionMeta{ID: uuid.New(), Model: "deepseek-flash"}

	page := ansi.Strip(renderStatsPage(m))
	if !strings.Contains(page, "the window of this session's model is not known") {
		t.Fatalf("stats page did not say the window is unknown:\n%s", page)
	}
}

// The page draws the sessions this one hangs off and owns as the tree the chat
// row draws them with: the arrow for the caller, branches for the children, the
// kind's glyph, and the status each one reported. The engine's index is what
// names a session this client never opened.
func TestStatsPageDrawsTheSessionTree(t *testing.T) {
	m := statsTestModel()
	parent, buildWorker, testWorker, closedWorker, unknown := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	m.session.ParentID = parent
	m.session.Offsprings = []uuid.UUID{buildWorker, testWorker, closedWorker, unknown}
	m.parentName, m.parentAgentKind = "Orchestrator", contracts.InteractiveAgent
	m.sidecars = []contracts.SidecarStatus{
		{ID: testWorker, Name: "test-worker", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionWaitingApproval},
	}
	m.recordIdentities([]contracts.SessionIdentity{
		{ID: parent, Name: "Orchestrator", AgentKind: contracts.InteractiveAgent, Status: contracts.SessionIdle},
		{ID: buildWorker, Name: "build-worker", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionCompleted},
		{ID: closedWorker, Name: "docs-writer", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionTombstone},
	})

	page := ansi.Strip(renderStatsPage(m))
	for _, want := range []string{
		"TREE",
		"↖ ⬢ Orchestrator",
		"○ idle",
		"├─ ⧉ build-worker",
		"✓ completed",
		"├─ ⧉ test-worker",
		"⚑ waiting for approval",
		"├─ ⧉ docs-writer",
		"· inactive",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("session tree lacks %q:\n%s", want, page)
		}
	}
	// A child nothing can name is still shown, by the id it was found by: a row
	// that says nothing is worse than one that says only which session it is.
	if !strings.Contains(page, "└─ "+unknown.String()) {
		t.Fatalf("an unnameable child was not shown by its full id:\n%s", page)
	}

	// A session with no caller and no children has no tree to draw.
	m.session.ParentID = uuid.Nil
	m.session.Offsprings = nil
	m.sidecars = nil
	if page := ansi.Strip(renderStatsPage(m)); strings.Contains(page, "TREE") {
		t.Fatalf("a session with no relatives drew a tree:\n%s", page)
	}
}

// A session the engine never answered for is drawn as the id it was found by,
// and the page says why: a bare id with no explanation reads as a session the
// harness cannot deal with, which is what a stale engine looks like from here.
func TestStatsPageSaysWhenTheEngineCouldNotNameARelative(t *testing.T) {
	m := statsTestModel()
	child, late := uuid.New(), uuid.New()
	m.session.Offsprings = []uuid.UUID{child}
	// A child that reported itself is named by its own report, so the note is
	// about the engine's silence rather than about the tree being nameless.
	m.sidecars = []contracts.SidecarStatus{{ID: late, Name: "test-worker", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionBusy}}
	m.modal = &modalModel{kind: modalStats, title: "Session stats", stats: m.sessionStats()}

	_, _ = m.Update(sessionIdentitiesMsg{err: errors.New("invalid session id")})
	page := ansi.Strip(renderStatsPage(m))
	for _, want := range []string{"├─ " + child.String(), "the engine did not answer: invalid session id", "└─ ⧉ test-worker"} {
		if !strings.Contains(page, want) {
			t.Fatalf("an unanswered tree lacks %q:\n%s", want, page)
		}
	}

	// The answer that does arrive takes the note with it.
	_, _ = m.Update(sessionIdentitiesMsg{sessions: []contracts.SessionIdentity{
		{ID: child, Name: "build-worker", AgentKind: contracts.SidecarLoopAgent, Status: contracts.SessionCompleted},
	}})
	page = ansi.Strip(renderStatsPage(m))
	if strings.Contains(page, "did not answer") {
		t.Fatalf("the note outlived the answer:\n%s", page)
	}
	if !strings.Contains(page, "├─ ⧉ build-worker") {
		t.Fatalf("the answer did not name the child:\n%s", page)
	}
}

// ctrl+d asks the engine about exactly the sessions the page draws: the caller,
// the children it recorded, and the children that have reported since.
func TestStatsPageAsksTheEngineWhoItsRelativesAre(t *testing.T) {
	parent, child, lateChild := uuid.New(), uuid.New(), uuid.New()
	client := &identityChatClient{}
	m := newChatModel(context.Background(), client, nil, RunOptions{})
	m.modal = nil
	m.width, m.height = 100, 30
	m.session = store.SessionMeta{ID: uuid.New(), Model: "deepseek-flash", ParentID: parent, Offsprings: []uuid.UUID{child}}
	m.sidecars = []contracts.SidecarStatus{{ID: lateChild, Status: contracts.SessionBusy}}

	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("ctrl+d did not ask the engine who the session's relatives are")
	}
	if msg, ok := cmd().(sessionIdentitiesMsg); !ok {
		t.Fatalf("the identity request answered %T, want a sessionIdentitiesMsg", msg)
	} else if len(msg.sessions) != 1 || msg.sessions[0].Name != "Orchestrator" {
		t.Fatalf("the identity request answered %+v, want the name the engine gave", msg.sessions)
	}
	if want := []uuid.UUID{parent, child, lateChild}; !slices.Equal(client.asked, want) {
		t.Fatalf("asked about %v, want %v", client.asked, want)
	}

	// A session with nobody to name asks about nobody.
	m.session.ParentID, m.session.Offsprings, m.sidecars = uuid.Nil, nil, nil
	client.asked = nil
	if msg, ok := sessionIdentitiesCmd(m.ctx, client, m.statsRelativeIDs())().(sessionIdentitiesMsg); !ok || len(msg.sessions) != 0 {
		t.Fatalf("a session with no relatives answered %+v", msg)
	}
	if len(client.asked) != 0 {
		t.Fatalf("a session with no relatives asked about %v", client.asked)
	}
}

// identityChatClient records the ids it was asked about, and names the first.
type identityChatClient struct {
	stubChatClient
	asked []uuid.UUID
}

func (c *identityChatClient) GetSessionIdentities(_ context.Context, ids []uuid.UUID) ([]contracts.SessionIdentity, error) {
	c.asked = ids
	return []contracts.SessionIdentity{{ID: ids[0], Name: "Orchestrator", AgentKind: contracts.InteractiveAgent, Status: contracts.SessionIdle}}, nil
}

// The session's acceptance script is shown whole, as the bash it is: the page
// scrolls rather than cutting it, so the script the session is judged by is the
// script the reader can read.
func TestStatsPageShowsTheAcceptanceScript(t *testing.T) {
	m := statsTestModel()
	script := "#!/usr/bin/env bash\nset -euo pipefail\n\n# every check must be read-only\ngo test ./pkg/agent/... -count=1\nbash build/verify.sh --strict"
	m.session.EvalBashScript = &script

	m.width, m.height = 100, 34
	page := ansi.Strip(renderStatsPage(m))
	for _, want := range []string{"ACCEPTANCE SCRIPT", "6 lines", "#!/usr/bin/env bash", "bash build/verify.sh --strict"} {
		if !strings.Contains(page, want) {
			t.Fatalf("acceptance script lacks %q:\n%s", want, page)
		}
	}

	// A session created without one has no script block to draw.
	m.session.EvalBashScript = nil
	if page := ansi.Strip(renderStatsPage(m)); strings.Contains(page, "ACCEPTANCE SCRIPT") {
		t.Fatalf("a session with no script drew one:\n%s", page)
	}
}

// A page longer than the terminal is scrolled, not shortened: every line it
// carries is reachable, the footer names the window, and scrolling stops on the
// last line instead of running past it.
func TestStatsPageScrollsItsWholeContent(t *testing.T) {
	m := statsTestModel()
	m.width, m.height = 100, 20
	last := "echo done-verifying"
	script := "#!/usr/bin/env bash\nset -euo pipefail\nfor i in 1 2 3 4 5; do\n  echo check-$i\ndone\n" + last
	m.session.EvalBashScript = &script

	page := ansi.Strip(renderStatsPage(m))
	if !strings.Contains(page, "1-") || !strings.Contains(page, "↑/↓ scroll") {
		t.Fatalf("a page with more to show did not name its window:\n%s", page)
	}
	if strings.Contains(page, last) {
		t.Fatalf("the first window already showed the last line:\n%s", page)
	}

	// Holding down rests on the last line rather than scrolling past it, and the
	// offset is clamped where it is kept: a page-down at the bottom leaves the
	// page where it was instead of parking the offset past the end.
	for range 40 {
		_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	}
	atEnd := m.modal.scroll
	bottom := ansi.Strip(renderStatsPage(m))
	if !strings.Contains(bottom, last) {
		t.Fatalf("scrolling to the bottom did not reach the last line:\n%s", bottom)
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if m.modal.scroll != atEnd {
		t.Fatalf("offset after a page-down at the end=%d want %d", m.modal.scroll, atEnd)
	}
	if again := ansi.Strip(renderStatsPage(m)); again != bottom {
		t.Fatalf("the page scrolled past its last line:\n%s", again)
	}

	// A page that fits has nothing to scroll, and does not say it does.
	m.session.EvalBashScript = nil
	m.height = 30
	if page := ansi.Strip(renderStatsPage(m)); strings.Contains(page, "scroll") || !strings.Contains(page, "esc back") {
		t.Fatalf("a page that fits still offered a scroll:\n%s", page)
	}
}

// A session that has ended says how and why it ended — but only while it is
// still in that state, and only for what the status in the heading does not
// already say. An end the session has since left is history, not state.
func TestStatsPageShowsHowTheSessionEnded(t *testing.T) {
	m := statsTestModel()
	m.status = contracts.SessionMaxIterationExhausted
	m.session.MaxTurns = ptr(uint64(40))
	m.session.Conclusion = &store.SessionConclusion{Status: contracts.SessionMaxIterationExhausted, Reason: "the sidecar exhausted its turn budget"}

	page := ansi.Strip(renderStatsPage(m))
	for _, want := range []string{"budget     40 turns", "ended      the sidecar exhausted its turn budget"} {
		if !strings.Contains(page, want) {
			t.Fatalf("end record lacks %q:\n%s", want, page)
		}
	}

	// An end with nothing to add to the status above it is not repeated.
	m.session.Conclusion = &store.SessionConclusion{Status: contracts.SessionMaxIterationExhausted}
	if page := ansi.Strip(renderStatsPage(m)); strings.Contains(page, "ended") {
		t.Fatalf("the end record repeated the status above it:\n%s", page)
	}

	// A session that was closed and reopened is running again, so its close is
	// not what it is doing: the record of it must not be shown as the session's
	// state, however it was ended.
	m.status = contracts.SessionWaitingApproval
	m.session.Conclusion = &store.SessionConclusion{Status: contracts.SessionTombstone, Reason: "session closed"}
	page = ansi.Strip(renderStatsPage(m))
	if strings.Contains(page, "ended") || strings.Contains(page, "session closed") {
		t.Fatalf("a reopened session still advertised its close:\n%s", page)
	}
	if !strings.Contains(page, "waiting for approval") {
		t.Fatalf("the page does not say what the session is doing:\n%s", page)
	}
}

// ptr returns a pointer to a value, for the optional fields of a session.
func ptr[T any](value T) *T { return &value }

// renderStatsPage draws a frame of the page the chat puts up for ctrl+d. A page
// already open keeps its scroll, and the session is re-read the way View does.
func renderStatsPage(m *chatModel) string {
	if m.modal == nil || m.modal.kind != modalStats {
		m.modal = &modalModel{
			kind: modalStats, title: "Session stats",
			body: "What this session is, how full its context is, and where the last turn's tokens went.",
		}
	}
	m.modal.stats = m.sessionStats()
	return renderSelectionScreen(m.modal, m.width, m.height, m.styles)
}

// The page is a full screen, and the session keeps reporting behind it: what it
// draws follows the session rather than freezing at the key that opened it.
func TestStatsPageFollowsTheSessionBehindIt(t *testing.T) {
	m := statsTestModel()
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if m.modal == nil {
		t.Fatal("ctrl+d did not open the session stats page")
	}

	report := `{"prompt_tokens":900,"completion_tokens":100,"total_tokens":1000}`
	m.applySessionEvent(contracts.EventStream{Kind: contracts.NotifyAgentUsage, Content: &report})

	page := ansi.Strip(m.View().Content)
	for _, want := range []string{"1k of 1M", "LAST TURN  1k tokens"} {
		if !strings.Contains(page, want) {
			t.Fatalf("stats page did not follow the session, %q missing from:\n%s", want, page)
		}
	}
}

// The page is drawn to whatever terminal it is given: its blocks are measured
// before they are rendered, so a heading or a note that wrapped would push the
// page past the rows and columns it has.
func TestStatsPageStaysWithinTheTerminal(t *testing.T) {
	m := statsTestModel()
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 20}, {44, 16}, {30, 12}} {
		m.width, m.height = size[0], size[1]
		page := renderStatsPanel(&modalModel{
			kind: modalStats, title: "Session stats",
			body:  "What this session is, how full its context is, and where the last turn's tokens went.",
			stats: m.sessionStats(),
		}, m.width, m.height, m.styles)
		if width := lipgloss.Width(page); width != size[0] {
			t.Errorf("%dx%d: page width=%d", size[0], size[1], width)
		}
		if height := lipgloss.Height(page); height != size[1] {
			t.Errorf("%dx%d: page height=%d", size[0], size[1], height)
		}
	}
}
