package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/memory"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

type runtimeTestModel struct {
	generate func(context.Context, []contracts.ChatMessage, []contracts.Tool, *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error)
	specs    contracts.ProvisionedModel
}

func newRuntimeTestModel(maxContext uint64) *runtimeTestModel {
	return &runtimeTestModel{
		specs: contracts.NewProvisionedModel(
			contracts.OpenAIGeneric,
			"test",
			"http://127.0.0.1",
			contracts.OpenAICompatableAPI,
			contracts.LLMProviderAuth{Method: contracts.NoneAuth},
			contracts.LLMModelConfiguration{Id: "test-model", MaxContextLength: maxContext, Modality: []contracts.LLMSupportedModality{contracts.ModalityText}},
		),
	}
}

func (m *runtimeTestModel) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	if m.generate != nil {
		return m.generate(ctx, messages, tools, opts)
	}
	return contracts.NewAssistantMessage("done"), &contracts.TokenUsage{}, nil
}

func (m *runtimeTestModel) GetModelSpecs() contracts.ProvisionedModel { return m.specs }

func newRuntimeTestTimeline(t *testing.T) *store.Timeline {
	t.Helper()
	timeline, err := store.NewTimeline(t.TempDir(), store.TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return timeline
}

func newBareRuntimeSession(t *testing.T, model contracts.InfaiModelAdaptor, timeline *store.Timeline, history []contracts.ChatMessage) *InfaiAgentSession {
	t.Helper()
	if model == nil {
		model = newRuntimeTestModel(1000)
	}
	if timeline == nil {
		timeline = newRuntimeTestTimeline(t)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	id := uuid.New()
	meta := store.SessionMeta{ID: id, Provider: "test", Model: "test-model", Cwd: t.TempDir(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	sessionStore, err := store.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	s := &InfaiAgentSession{
		l:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		ctx:            ctx,
		cancel:         cancel,
		closeDone:      make(chan struct{}),
		meta:           meta,
		status:         contracts.SessionIdle,
		model:          model,
		timeline:       timeline,
		store:          sessionStore,
		taskChecklist:  memory.NewTaskChecklist(),
		activeTimeline: append([]contracts.ChatMessage(nil), history...),
		eventBus:       make(chan contracts.EventStream, 256),
		subscribers:    make(map[*subscriber]struct{}),
	}
	s.agent, err = agent.NewAgent(model, contracts.InteractiveAgent, s.commitMessages, s.eventBus, func([]contracts.ToolCall) []contracts.ChatMessage { return nil }, "test system prompt", agent.WithMaxTurns(100), agent.WithAutoCompaction(s.shouldCompact, s.autoCompact))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startRuntimeSession(s *InfaiAgentSession) {
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.handlerForSessionEvents(s.ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.agent.StartLoop(s.ctx, s.activeTimeline)
	}()
}

func waitForSessionStatus(t *testing.T, s *InfaiAgentSession, want contracts.SessionStatus) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if s.Status() == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("session status = %q, want %q", s.Status(), want)
		case <-ticker.C:
		}
	}
}

func receiveEvent(t *testing.T, events <-chan contracts.EventStream) contracts.EventStream {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return contracts.EventStream{}
	}
}

func TestJoinSessionFanoutLimitAndSlotReuse(t *testing.T) {
	s := newBareRuntimeSession(t, nil, nil, nil)
	defer s.Close()

	_, first, unsubscribeFirst, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	_, second, unsubscribeSecond, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSecond()
	if _, _, _, err := s.JoinSessionEvents(); !errors.Is(err, harnessErr.ErrTooManyClients) {
		t.Fatalf("third join error = %v, want ErrTooManyClients", err)
	}

	content := "hello"
	s.mu.Lock()
	s.notifySubscribers(contracts.EventStream{Kind: contracts.DeltaContent, Timestamp: time.Now().UTC(), Content: &content})
	s.mu.Unlock()
	if got := receiveEvent(t, first); got.Content == nil || *got.Content != content {
		t.Fatalf("first subscriber event = %+v", got)
	}
	if got := receiveEvent(t, second); got.Content == nil || *got.Content != content {
		t.Fatalf("second subscriber event = %+v", got)
	}

	unsubscribeFirst()
	_, replacement, unsubscribeReplacement, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatalf("join after unsubscribe: %v", err)
	}
	unsubscribeReplacement()
	if _, ok := <-replacement; ok {
		t.Fatal("replacement subscription remained open after unsubscribe")
	}
}

func TestJoinSessionSnapshotBoundary(t *testing.T) {
	s := newBareRuntimeSession(t, nil, nil, nil)
	// Only the hub runs: this is about how a join and a fan-out interleave, and
	// a running agent loop would also be publishing into the same subscriber.
	s.wg.Go(func() {
		s.handlerForSessionEvents(s.ctx)
	})
	defer s.Close()

	for i := range 100 {
		s.mu.Lock()
		s.inFlight = nil
		s.mu.Unlock()

		content := "x"
		s.publish(contracts.EventStream{Kind: contracts.DeltaContent, Timestamp: time.Now().UTC(), Content: &content})

		view, events, unsubscribe, err := s.JoinSessionEvents()
		if err != nil {
			t.Fatal(err)
		}
		// The change is either in the log the join was handed or still on its
		// way to this client, never both and never neither.
		if len(view.InFlight) == 1 {
			select {
			case event := <-events:
				t.Fatalf("iteration %d observed change in snapshot and event: %+v", i, event)
			default:
			}
		} else {
			if len(view.InFlight) != 0 {
				t.Fatalf("iteration %d snapshot carried %d events", i, len(view.InFlight))
			}
			event := receiveEvent(t, events)
			if event.Content == nil || *event.Content != "x" {
				t.Fatalf("iteration %d event = %+v", i, event)
			}
		}
		unsubscribe()
	}
}

func TestSlowSubscriberGetsGapWithoutBlockingPeer(t *testing.T) {
	s := newBareRuntimeSession(t, nil, nil, nil)
	defer s.Close()

	_, slow, unsubscribeSlow, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSlow()
	_, fast, unsubscribeFast, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFast()

	for i := 0; i <= SUBSCRIBER_BUFFER; i++ {
		content := "event"
		s.mu.Lock()
		s.notifySubscribers(contracts.EventStream{Kind: contracts.DeltaContent, Timestamp: time.Now().UTC(), Content: &content})
		s.mu.Unlock()
		if event := receiveEvent(t, fast); event.Kind != contracts.DeltaContent {
			t.Fatalf("fast subscriber event kind = %q", event.Kind)
		}
		runtime.Gosched()
	}

	if event := receiveEvent(t, slow); event.Kind != contracts.EventSubscriberGap {
		t.Fatalf("slow subscriber event kind = %q, want subscriber gap", event.Kind)
	}
	if _, ok := <-slow; ok {
		t.Fatal("slow subscriber channel remained open after gap")
	}
	if _, _, unsubscribe, err := s.JoinSessionEvents(); err != nil {
		t.Fatalf("slow subscriber did not free its slot: %v", err)
	} else {
		unsubscribe()
	}
}

func TestApprovalSurvivesObserverDisconnectAndCanBeResolved(t *testing.T) {
	s := newBareRuntimeSession(t, nil, nil, nil)
	startRuntimeSession(s)
	defer s.Close()

	// The agent reports busy before it dispatches tools, and performHITL is
	// reached from inside that dispatch. The loop is parked here, so the turn
	// is modelled by reporting that status the way the loop would.
	busy := string(contracts.SessionBusy)
	s.publish(contracts.EventStream{Kind: contracts.EventSessionTransitionState, Timestamp: time.Now().UTC(), Content: &busy})

	result := make(chan error, 1)
	go func() {
		result <- s.performHITL(s.ctx, s.meta.ID, contracts.ToolCall{ID: "call-1", Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"pwd"}`}})
	}()
	waitForSessionStatus(t, s, contracts.SessionWaitingApproval)

	view, _, unsubscribe, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	if view.PendingApproval == nil {
		t.Fatal("pending approval missing from joined view")
	}
	request := *view.PendingApproval
	unsubscribe()

	view, events, unsubscribe, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if view.PendingApproval == nil || view.PendingApproval.ID != request.ID {
		t.Fatalf("approval did not survive observer disconnect: %+v", view.PendingApproval)
	}
	if err := s.ResolveApproval(request.ID, contracts.ApprovalConclusion{ReqID: request.ID, Fingerprint: request.Fingerprint, Decision: contracts.ApprovalApprove}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("performHITL returned %v", err)
	}
	if event := receiveEvent(t, events); event.Kind != contracts.EventApprovalResolved {
		t.Fatalf("approval event kind = %q", event.Kind)
	}
	waitForSessionStatus(t, s, contracts.SessionBusy)
}

func TestManualCompactionRejectsChatAndReplacesHistory(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := newRuntimeTestModel(1000)
	model.generate = func(ctx context.Context, _ []contracts.ChatMessage, _ []contracts.Tool, _ *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
		close(started)
		select {
		case <-release:
			return contracts.NewAssistantMessage("summary"), &contracts.TokenUsage{}, nil
		case <-ctx.Done():
			return contracts.ChatMessage{}, nil, ctx.Err()
		}
	}
	history := []contracts.ChatMessage{contracts.NewUserMessage("old"), contracts.NewAssistantMessage("answer")}
	s := newBareRuntimeSession(t, model, nil, history)
	startRuntimeSession(s)
	defer s.Close()
	waitForSessionStatus(t, s, contracts.SessionIdle)

	compacted := make(chan error, 1)
	go func() { compacted <- s.CompactChat(context.Background()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("compaction summarizer did not start")
	}
	waitForSessionStatus(t, s, contracts.SessionCompacting)
	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "too soon"}); err == nil || !strings.Contains(err.Error(), "compacting") {
		t.Fatalf("chat during compaction error = %v", err)
	}
	close(release)
	if err := <-compacted; err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, s, contracts.SessionIdle)

	view, _, unsubscribe, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(view.History) != 1 || !strings.Contains(view.History[0].Text(), "summary") {
		t.Fatalf("compacted history = %+v", view.History)
	}
}

func TestAutomaticCompactionFailureCancelsWaitingAgent(t *testing.T) {
	model := newRuntimeTestModel(10)
	model.generate = func(_ context.Context, _ []contracts.ChatMessage, _ []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
		if opts != nil && opts.Stream {
			return contracts.NewAssistantMessage("answer"), &contracts.TokenUsage{TotalTokens: 10}, nil
		}
		return contracts.ChatMessage{}, nil, errors.New("summarizer failed")
	}
	history := []contracts.ChatMessage{
		contracts.NewUserMessage("one"), contracts.NewAssistantMessage("two"),
		contracts.NewUserMessage("three"), contracts.NewAssistantMessage("four"),
		contracts.NewUserMessage("five"), contracts.NewAssistantMessage("six"),
	}
	s := newBareRuntimeSession(t, model, nil, history)
	startRuntimeSession(s)
	defer s.Close()
	waitForSessionStatus(t, s, contracts.SessionIdle)

	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "trigger"}); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, s, contracts.SessionTombstone)
	waitForSessionCancellation(t, s, "summarizer failed")
}

func waitForSessionCancellation(t *testing.T, s *InfaiAgentSession, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cause := context.Cause(s.ctx); cause != nil {
			if !strings.Contains(cause.Error(), want) {
				t.Fatalf("session cancellation cause = %v, want it to contain %q", cause, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session context was never canceled, want cause containing %q", want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSelectBranchRejectsQueuedPrompt(t *testing.T) {
	timeline := newRuntimeTestTimeline(t)
	selected, err := timeline.AppendToHead(store.Record{Kind: store.KindMessage, Message: ptrMessage(contracts.NewUserMessage("branch point"))})
	if err != nil {
		t.Fatal(err)
	}
	s := newBareRuntimeSession(t, nil, timeline, nil)
	defer s.Close()
	if err := s.agent.Enqueue(context.Background(), contracts.NewUserMessage("already queued")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectBranch(selected.ID); err == nil || !strings.Contains(err.Error(), "queued messages") {
		t.Fatalf("branch selection error = %v", err)
	}
}

func TestCloseCancelsPendingApprovalAndClosesSubscribers(t *testing.T) {
	s := newBareRuntimeSession(t, nil, nil, nil)
	startRuntimeSession(s)
	approvalResult := make(chan error, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		approvalResult <- s.performHITL(s.ctx, s.meta.ID, contracts.ToolCall{ID: "call-1", Function: contracts.Function{Name: contracts.BashTool}})
	}()
	waitForSessionStatus(t, s, contracts.SessionWaitingApproval)
	_, events, _, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session close blocked on pending approval")
	}
	if err := <-approvalResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("pending approval result = %v", err)
	}
	for range events {
	}
	if s.Status() != contracts.SessionTombstone {
		t.Fatalf("closed session status = %q", s.Status())
	}
}

// TestBranchSwitchAppliesToFirstPromptAfterSwitch checks that the prompt
// submitted immediately after a branch selection is answered using the branch
// history, not the pre-branch history.
func TestBranchSwitchAppliesToFirstPromptAfterSwitch(t *testing.T) {
	generated := make(chan []contracts.ChatMessage, 4)
	model := newRuntimeTestModel(1000)
	model.generate = func(_ context.Context, messages []contracts.ChatMessage, _ []contracts.Tool, _ *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
		generated <- append([]contracts.ChatMessage(nil), messages...)
		return contracts.NewAssistantMessage("ok"), &contracts.TokenUsage{}, nil
	}

	timeline := newRuntimeTestTimeline(t)
	first, err := timeline.AppendToHead(store.Record{Kind: store.KindMessage, Message: ptrMessage(contracts.NewUserMessage("one"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := timeline.AppendToHead(store.Record{Kind: store.KindMessage, Message: ptrMessage(contracts.NewAssistantMessage("two"))}); err != nil {
		t.Fatal(err)
	}

	s := newBareRuntimeSession(t, model, timeline, []contracts.ChatMessage{contracts.NewUserMessage("one"), contracts.NewAssistantMessage("two")})
	startRuntimeSession(s)
	defer s.Close()
	waitForSessionStatus(t, s, contracts.SessionIdle)
	// The loop publishes Idle before it parks, so give it time to actually be
	// blocked in its select before switching the branch.
	time.Sleep(100 * time.Millisecond)

	if _, err := s.SelectBranch(first.ID); err != nil {
		t.Fatalf("select branch: %v", err)
	}
	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "after branch"}); err != nil {
		t.Fatalf("enqueue after branch: %v", err)
	}

	var seen []contracts.ChatMessage
	select {
	case seen = <-generated:
	case <-time.After(2 * time.Second):
		t.Fatal("no generation after branch switch")
	}
	for i, message := range seen {
		if message.Role == "assistant" && message.Text() == "two" {
			t.Fatalf("model saw pre-branch history at index %d: %+v", i, seen)
		}
	}
}

// TestManualCompactionAppliesToFirstPromptAfterCompaction checks that the
// prompt submitted right after a manual compaction is answered from the
// compacted history, not the unfolded one.
func TestManualCompactionAppliesToFirstPromptAfterCompaction(t *testing.T) {
	generated := make(chan []contracts.ChatMessage, 4)
	model := newRuntimeTestModel(0) // zero context window disables auto-compaction
	model.generate = func(_ context.Context, messages []contracts.ChatMessage, _ []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
		if opts == nil || !opts.Stream {
			// The compaction summarizer shares this model client.
			return contracts.NewAssistantMessage("checkpoint"), &contracts.TokenUsage{}, nil
		}
		generated <- append([]contracts.ChatMessage(nil), messages...)
		return contracts.NewAssistantMessage("ok"), &contracts.TokenUsage{}, nil
	}

	s := newBareRuntimeSession(t, model, nil, []contracts.ChatMessage{contracts.NewUserMessage("one"), contracts.NewAssistantMessage("two")})
	startRuntimeSession(s)
	defer s.Close()
	waitForSessionStatus(t, s, contracts.SessionIdle)
	// The loop publishes Idle before it parks, so give it time to actually be
	// blocked in its select before compacting.
	time.Sleep(100 * time.Millisecond)

	if err := s.CompactChat(context.Background()); err != nil {
		t.Fatalf("manual compaction: %v", err)
	}
	waitForSessionStatus(t, s, contracts.SessionIdle)
	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "after compact"}); err != nil {
		t.Fatalf("enqueue after compaction: %v", err)
	}

	var seen []contracts.ChatMessage
	select {
	case seen = <-generated:
	case <-time.After(2 * time.Second):
		t.Fatal("no generation after manual compaction")
	}
	if len(seen) < 2 || !strings.HasPrefix(seen[1].Text(), "<context-summary>") {
		t.Fatalf("model did not see the compacted history: %+v", seen)
	}
}

// TestEnqueueWhileRunningQueuesForNextTurn checks that a prompt submitted while
// a turn is already in flight is accepted and answered on the next turn, using
// the working history in place at that point.
func TestEnqueueWhileRunningQueuesForNextTurn(t *testing.T) {
	firstCall := make(chan struct{})
	release := make(chan struct{})
	generated := make(chan []contracts.ChatMessage, 8)
	var calls atomic.Int32

	model := newRuntimeTestModel(0)
	model.generate = func(_ context.Context, messages []contracts.ChatMessage, _ []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
		if opts == nil || !opts.Stream {
			return contracts.NewAssistantMessage("checkpoint"), &contracts.TokenUsage{}, nil
		}
		generated <- append([]contracts.ChatMessage(nil), messages...)
		if calls.Add(1) == 1 {
			close(firstCall)
			<-release
		}
		return contracts.NewAssistantMessage("reply"), &contracts.TokenUsage{}, nil
	}

	s := newBareRuntimeSession(t, model, nil, nil)
	startRuntimeSession(s)
	defer s.Close()
	waitForSessionStatus(t, s, contracts.SessionIdle)
	time.Sleep(100 * time.Millisecond)

	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "P1"}); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	select {
	case <-firstCall:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn never started")
	}

	waitForSessionStatus(t, s, contracts.SessionBusy)

	// The turn is in flight: the second prompt must be accepted, not rejected.
	if err := s.EnqueueUserMessage(context.Background(), contracts.UserInput{Text: "P2"}); err != nil {
		t.Fatalf("prompt while running was rejected: %v", err)
	}
	if got := s.Status(); got != contracts.SessionBusy {
		t.Fatalf("status after queued prompt = %q, want busy", got)
	}

	close(release)

	var second []contracts.ChatMessage
	select {
	case <-generated: // first turn
		select {
		case second = <-generated:
		case <-time.After(2 * time.Second):
			t.Fatal("queued prompt was never answered")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no generation observed")
	}
	found := false
	for _, message := range second {
		if message.Role == "user" && message.Text() == "P2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("second turn did not include the queued prompt: %+v", second)
	}
}

func ptrMessage(message contracts.ChatMessage) *contracts.ChatMessage { return &message }
