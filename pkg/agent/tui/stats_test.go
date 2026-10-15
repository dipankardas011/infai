package tui

import (
	"context"
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

// renderStatsPage draws the page the chat puts up for ctrl+d.
func renderStatsPage(m *chatModel) string {
	m.modal = &modalModel{kind: modalStats, title: "Session stats", stats: m.sessionStats()}
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
