package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

var (
	ErrSessionClosed   = errors.New("session closed")
	ErrNoProvider      = errors.New("engine: no provider configured")
	ErrCompactionLimit = errors.New("session: automatic compaction limit reached")
)

const maxAutomaticCompactions = 2

// InfaiAgentSession is the persistent state of one conversation. It is a
// passive object: it holds history and the agent tree, and Chat() runs the
// loop against it. Between Chat calls the session is idle — no goroutine is
// held — so it stays registered until explicitly closed. Every turn is
// streamed through the session's event hub, which fans out to the live user
// sink (SSE/stdout); durable history is written directly to the timeline.
type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	model contracts.InfaiModelAdaptor

	mu      sync.Mutex
	closed  bool
	history []contracts.ChatMessage

	meta                store.SessionMeta
	timeline            *store.Timeline
	store               *store.SessionStore
	events              *store.SessionEventHub
	persisted           int
	pendingBranchParent uuid.UUID

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentComms        // By this when N no of children can send comms with engine and even the

	baseAgentId uuid.UUID

	Agents map[uuid.UUID]*agent.Agent
}

// NewSession creates a fresh session bound to the given provider and model.
func NewSession(l *slog.Logger, p *store.Provider, model string, ctxWindow int, cwd string, ss *store.SessionStore) (*InfaiAgentSession, error) {
	if p == nil {
		return nil, ErrNoProvider
	}

	o := &InfaiAgentSession{
		l:             l,
		model:         models.NewOpenAICompatableAPI(p.Endpoint, model, p.APIKey),
		store:         ss,
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*agent.Agent),
	}

	if v, err := uuid.NewV7(); err != nil {
		return nil, err
	} else {
		o.sessionID = v
	}

	now := time.Now().UTC()
	o.meta = store.SessionMeta{
		ID:            o.sessionID,
		Provider:      p.Name,
		Model:         model,
		Cwd:           cwd,
		ContextWindow: ctxWindow,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	o.events = store.NewSessionEventHub()
	if err := ss.SaveMeta(o.meta); err != nil {
		return nil, err
	}

	timeline, err := ss.LoadSessionTimelineClient(o.meta.ID)
	if err != nil {
		return nil, err
	}
	o.timeline = timeline

	if err := o.registerBaseAgent(); err != nil {
		return nil, err
	}
	return o, nil
}

// NewResumedSession rebuilds a session from the active timeline ancestry. The
// caller resolves lazy blob records before constructing the chat history.
func NewResumedSession(l *slog.Logger, p *store.Provider, meta store.SessionMeta, history []contracts.ChatMessage, timeline *store.Timeline, sessionStore *store.SessionStore) (*InfaiAgentSession, error) {
	if p == nil {
		return nil, ErrNoProvider
	}

	o := &InfaiAgentSession{
		l:             l,
		model:         models.NewOpenAICompatableAPI(p.Endpoint, meta.Model, p.APIKey),
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*agent.Agent),
		sessionID:     meta.ID,
		meta:          meta,
		store:         sessionStore,
		history:       history,
		persisted:     len(history),
		timeline:      timeline,
		events:        store.NewSessionEventHub(),
	}

	if err := sessionStore.SaveMeta(meta); err != nil {
		return nil, err
	}
	if err := o.registerBaseAgent(); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *InfaiAgentSession) registerBaseAgent() error {
	firstAgent, err := s.registerNewParentAgent()
	if err != nil {
		return err
	}
	s.baseAgentId = firstAgent.Id
	return nil
}

func (s *InfaiAgentSession) ID() uuid.UUID {
	return s.sessionID
}

func (s *InfaiAgentSession) Meta() store.SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// EventHub exposes the session's live broadcaster so the server can attach the
// live SSE sink per request.
func (s *InfaiAgentSession) EventHub() *store.SessionEventHub {
	return s.events
}

// Timeline returns the full branch ancestry as lightweight events. Blob-backed
// records remain placeholders until explicitly resolved.
func (s *InfaiAgentSession) Timeline() ([]store.Event, uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.timeline.LoadFullSessionTimeline()
	return events, s.timeline.CurrentHeadEventID(), err
}

func (s *InfaiAgentSession) SelectBranch(eventID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.timeline.LoadEvent(eventID); err != nil {
		return err
	}
	s.pendingBranchParent = eventID
	return nil
}

// SetModel rebuilds the session's model adapter for the given provider and
// model and records the change in the session meta.
func (s *InfaiAgentSession) SetModel(p *store.Provider, name string, ctxWindow int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setModelLocked(p, name, ctxWindow)
}

func (s *InfaiAgentSession) setModelLocked(p *store.Provider, name string, ctxWindow int) {
	if p == nil {
		return
	}
	s.model = models.NewOpenAICompatableAPI(p.Endpoint, name, p.APIKey)
	if a := s.Agents[s.baseAgentId]; a != nil {
		a.SetModel(s.model)
	}
	s.meta.Provider = p.Name
	s.meta.Model = name
	s.meta.ContextWindow = ctxWindow
	s.meta.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveMeta(s.meta); err != nil {
		s.l.Error("persist session metadata", "session_id", s.sessionID, "error", err)
	}
}

// Chat runs the base agent loop for one user prompt against the session's
// persistent history and returns the outcome. The session stays registered
// and idle after the call; the next Chat reuses the same conversation. New
// messages, usage and meta are persisted through the recorder as they settle.
func (s *InfaiAgentSession) Chat(ctx context.Context, prompt string, opts ChatOptions) (*ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrSessionClosed
	}
	parentID := s.timeline.CurrentHeadEventID()
	selectedBranch := s.pendingBranchParent != uuid.Nil
	if s.pendingBranchParent != uuid.Nil {
		events, err := s.timeline.LoadActiveSessionTimelineAt(s.pendingBranchParent)
		if err != nil {
			return nil, err
		}
		history, err := timelineHistory(s.timeline, events)
		if err != nil {
			return nil, err
		}
		s.history = history
		s.persisted = len(history)
		parentID = s.pendingBranchParent
		s.pendingBranchParent = uuid.Nil
	}

	if len(s.history) == 0 {
		sysPrompt, err := GetBasicSystemPrompt(nil, nil)
		if err != nil {
			return nil, err
		}
		sysMsg := contracts.NewSystemMessage(sysPrompt)
		s.history = append(s.history, sysMsg)
		if _, err := s.timeline.AppendToHead(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &sysMsg}); err != nil {
			s.l.Error("persist system prompt", "session_id", s.sessionID, "error", err)
		}
		if !selectedBranch {
			parentID = s.timeline.CurrentHeadEventID()
		}
	}
	s.history = append(s.history, contracts.NewUserMessage(prompt))

	// Persist the user's message before generation so a hard crash can never
	// lose what was typed. The reply is synced at turn end; an interrupted
	// reply is the only thing a crash can take.
	msg := s.history[len(s.history)-1]
	var appendErr error
	if parentID == s.timeline.CurrentHeadEventID() {
		_, appendErr = s.timeline.AppendToHead(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &msg})
	} else {
		_, appendErr = s.timeline.BranchFromEventID(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &msg}, parentID)
	}
	if appendErr != nil {
		s.l.Error("persist user message", "session_id", s.sessionID, "error", appendErr)
	}
	s.persisted = len(s.history)

	agentLoop := s.Agents[s.baseAgentId]
	if agentLoop == nil {
		return nil, errors.New("session: base agent missing")
	}
	agentLoop.SetDeltaHook(func(kind contracts.DeltaKind, text string) {
		k := "content"
		if kind == contracts.DeltaReasoning {
			k = "reasoning"
		}
		s.events.Publish(store.Record{Kind: store.KindDelta, Timestamp: time.Now().UTC(), DeltaKind: k, Text: text})
	})

	var result agent.TurnResult
	var invokeErr error
	for compactions := 0; ; compactions++ {
		result, invokeErr = agentLoop.Invoke(ctx, s.history)
		if result.Messages != nil {
			s.history = result.Messages
			s.persistMessagesLocked()
		}
		if invokeErr != nil {
			return nil, invokeErr
		}
		if !s.shouldCompact(result.Usage) {
			break
		}
		if compactions >= maxAutomaticCompactions {
			return nil, ErrCompactionLimit
		}
		if err := s.compactLocked(ctx, true); err != nil {
			return nil, err
		}
	}
	s.meta.TurnCount++
	s.meta.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveMeta(s.meta); err != nil {
		s.l.Error("persist session metadata", "session_id", s.sessionID, "error", err)
	}

	return &ChatResult{
		SessionID:        s.sessionID,
		Status:           result.Status,
		Reply:            lastAssistantText(result.Messages),
		ReasoningContent: lastAssistantReasoning(result.Messages),
		Pending:          result.Pending,
		Usage:            result.Usage,
	}, nil
}

// persistMessagesLocked writes history entries beyond the watermark as message
// records, deriving tool-call and tool-result records when present. A
// persistence failure is logged; the in-memory history is advanced either way
// so the session keeps working. Caller holds s.mu.
func (s *InfaiAgentSession) persistMessagesLocked() {
	for i := s.persisted; i < len(s.history); i++ {
		m := s.history[i]
		if _, err := s.timeline.AppendToHead(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &m}); err != nil {
			s.l.Error("persist message", "session_id", s.sessionID, "error", err)
		}
	}
	s.persisted = len(s.history)
}

func (s *InfaiAgentSession) resetHistoryLocked(summary string) {
	system := ""
	if len(s.history) > 0 && s.history[0].Role == "system" {
		system = s.history[0].Text()
	}
	s.history = []contracts.ChatMessage{contracts.NewSystemMessage(system), contracts.NewSystemMessage("Previous context summary:\n" + summary)}
	s.persisted = len(s.history)
	s.l.Info("session context compacted", "session_id", s.sessionID, "context_window", s.meta.ContextWindow)
}

// Compact creates and persists a continuation summary without adding a
// summarizer request to the normal conversation history.
func (s *InfaiAgentSession) Compact(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	return s.compactLocked(ctx, false)
}

func (s *InfaiAgentSession) shouldCompact(usage *contracts.TokenUsage) bool {
	return usage != nil && s.meta.ContextWindow > 0 && usage.PromptTokens*100 >= s.meta.ContextWindow*80
}

func (s *InfaiAgentSession) compactLocked(ctx context.Context, automatic bool) error {
	result, err := s.summarize(ctx, s.history, automatic)
	if err != nil {
		return err
	}
	if result.Summary == "" {
		return errors.New("session: compaction produced an empty summary")
	}
	if _, err := s.timeline.AppendToHead(store.Record{
		Kind: store.KindCompaction, Timestamp: time.Now().UTC(),
		Compaction: &store.CompactionRecord{Summary: result.Summary},
	}); err != nil {
		return err
	}
	s.resetHistoryLocked(result.Summary)
	return nil
}

func (s *InfaiAgentSession) summarize(ctx context.Context, history []contracts.ChatMessage, automatic bool) (agent.CompactionResult, error) {
	prompt := ManualCompactionPrompt(history)
	if automatic {
		prompt = AutomaticCompactionPrompt(history)
	}
	reply, _, err := s.model.Generate(ctx, prompt, &contracts.GenerateOptions{})
	if err != nil {
		return agent.CompactionResult{}, err
	}
	return agent.CompactionResult{Summary: reply.Text()}, nil
}

func (s *InfaiAgentSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.events != nil {
		s.events.Close()
	}
	if s.timeline != nil {
		_ = s.timeline.Close()
	}
}

func (s *InfaiAgentSession) registerNewParentAgent() (*agent.Agent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	s.agentMapping[id] = ds.NewSet[uuid.UUID]()
	s.runtime_comms[id] = FreshAgentComms()
	s.Agents[id], err = agent.NewAgent(id, agent.WithMaxTurns(100))
	if err != nil {
		return nil, err
	}
	s.Agents[id].SetModel(s.model)
	return s.Agents[id], nil
}

// lastAssistantText extracts the agent's answer from the full history. It
// lives here (the API boundary) because the wire format wants a plain reply
// string — the agent itself only returns history.
func lastAssistantText(messages []contracts.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Text()
		}
	}
	return ""
}

// lastAssistantReasoning extracts the reasoning text of the final assistant
// message, or "" when the provider returned none.
func lastAssistantReasoning(messages []contracts.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if m := messages[i]; m.Role == "assistant" && m.ReasoningContent != "" {
			return m.ReasoningContent
		}
	}
	return ""
}

func timelineHistory(timeline *store.Timeline, events []store.Event) ([]contracts.ChatMessage, error) {
	// Timeline loads events lazily so inspection stays cheap. Resuming a
	// session is the boundary where blob-backed records must become messages.
	records, err := timelineRecords(timeline, events)
	if err != nil {
		return nil, err
	}
	var history []contracts.ChatMessage
	hasSystem := false
	for _, record := range records {
		switch record.Kind {
		case store.KindMessage:
			if record.Message != nil {
				if record.Message.Role == "system" {
					hasSystem = true
				}
				history = append(history, *record.Message)
			}
		case store.KindCompaction:
			if record.Compaction != nil {
				if !hasSystem {
					systemPrompt, _ := GetBasicSystemPrompt(nil, nil)
					history = append(history, contracts.NewSystemMessage(systemPrompt))
					hasSystem = true
				}
				history = append(history, contracts.NewSystemMessage("Previous context summary:\n"+record.Compaction.Summary))
			}
		}
	}
	return history, nil
}

func timelineRecords(timeline *store.Timeline, events []store.Event) ([]store.Record, error) {
	records := make([]store.Record, 0, len(events))
	for _, event := range events {
		record := event.Record
		if record == nil {
			resolved, err := timeline.ResolveRecord(event)
			if err != nil {
				return nil, err
			}
			record = &resolved
		}
		records = append(records, *record)
	}
	return records, nil
}
