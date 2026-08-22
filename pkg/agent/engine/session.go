package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

var (
	ErrSessionClosed = errors.New("session closed")
	ErrNoProvider    = errors.New("engine: no provider configured")
)

// InfaiAgentSession is the persistent state of one conversation. It is a
// passive object: it holds history and the agent tree, and Chat() runs the
// loop against it. Between Chat calls the session is idle — no goroutine is
// held — so it stays registered until explicitly closed. Every turn is
// streamed through the session's Recorder, which fans out to the live user
// sink (SSE/stdout) and appends the durable transcript to disk.
type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	model contracts.InfaiModelAdaptor

	mu      sync.Mutex
	closed  bool
	history []contracts.ChatMessage

	meta      store.SessionMeta
	rec       *store.Recorder
	persisted int

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
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*agent.Agent),
	}

	if v, err := uuid.NewV7(); err != nil {
		return nil, err
	} else {
		o.sessionID = v
	}

	now := store.NowISO()
	o.meta = store.SessionMeta{
		ID:            o.sessionID,
		Provider:      p.Name,
		Model:         model,
		Cwd:           cwd,
		ContextWindow: ctxWindow,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := o.openRecorder(ss); err != nil {
		return nil, err
	}
	if err := o.registerBaseAgent(); err != nil {
		return nil, err
	}
	return o, nil
}

// NewResumedSession rebuilds a session from a saved transcript (meta + history).
func NewResumedSession(l *slog.Logger, p *store.Provider, meta store.SessionMeta, history []contracts.ChatMessage, ss *store.SessionStore) (*InfaiAgentSession, error) {
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
		history:       history,
		persisted:     len(history),
	}

	if err := o.openRecorder(ss); err != nil {
		return nil, err
	}
	if err := o.registerBaseAgent(); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *InfaiAgentSession) openRecorder(ss *store.SessionStore) error {
	rec, err := ss.OpenRecorder(s.meta)
	if err != nil {
		return err
	}
	s.rec = rec
	return nil
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

// Recorder exposes the session's multi-writer so the server can attach the
// live SSE sink per request.
func (s *InfaiAgentSession) Recorder() *store.Recorder {
	return s.rec
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
	s.meta.UpdatedAt = store.NowISO()
	if err := s.rec.Record(store.Record{Kind: store.KindMeta, Ts: s.meta.UpdatedAt, Meta: &s.meta}); err != nil {
		s.l.Error("persist session meta", "session_id", s.sessionID, "error", err)
	}
	if err := s.rec.Sync(); err != nil {
		s.l.Error("sync session meta", "session_id", s.sessionID, "error", err)
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

	if len(s.history) == 0 {
		sysPrompt, err := GetBasicSystemPrompt(nil, nil)
		if err != nil {
			return nil, err
		}
		sysMsg := contracts.NewSystemMessage(sysPrompt)
		s.history = append(s.history, sysMsg)
		if err := s.rec.Record(store.Record{Kind: store.KindMessage, Ts: store.NowISO(), Message: &sysMsg}); err != nil {
			s.l.Error("persist system prompt", "session_id", s.sessionID, "error", err)
		}
	}
	s.history = append(s.history, contracts.NewUserMessage(prompt))

	// Persist the user's message before generation so a hard crash can never
	// lose what was typed. The reply is synced at turn end; an interrupted
	// reply is the only thing a crash can take.
	msg := s.history[len(s.history)-1]
	if err := s.rec.Record(store.Record{Kind: store.KindMessage, Ts: store.NowISO(), Message: &msg}); err != nil {
		s.l.Error("persist user message", "session_id", s.sessionID, "error", err)
	}
	if err := s.rec.Sync(); err != nil {
		s.l.Error("sync user message", "session_id", s.sessionID, "error", err)
	}
	s.persisted = len(s.history)

	a := s.Agents[s.baseAgentId]
	if a == nil {
		return nil, errors.New("session: base agent missing")
	}
	a.SetDeltaHook(func(kind contracts.DeltaKind, text string) {
		k := "content"
		if kind == contracts.DeltaReasoning {
			k = "reasoning"
		}
		s.rec.Record(store.Record{Kind: store.KindDelta, Ts: store.NowISO(), DeltaKind: k, Text: text})
	})

	result, err := a.Invoke(ctx, s.history)
	if err != nil {
		return nil, err
	}
	s.history = result.Messages

	s.persistMessagesLocked()
	s.meta.TurnCount++
	s.meta.UpdatedAt = store.NowISO()
	if err := s.rec.Record(store.Record{Kind: store.KindMeta, Ts: s.meta.UpdatedAt, Meta: &s.meta}); err != nil {
		s.l.Error("persist session meta", "session_id", s.sessionID, "error", err)
	}
	// One fsync per finished turn: by the time Chat returns, this turn is
	// durable, so a crash never loses a completed conversation.
	if err := s.rec.Sync(); err != nil {
		s.l.Error("sync session", "session_id", s.sessionID, "error", err)
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
		if err := s.rec.Record(store.Record{Kind: store.KindMessage, Ts: store.NowISO(), Message: &m}); err != nil {
			s.l.Error("persist message", "session_id", s.sessionID, "error", err)
		}
		for _, tc := range m.ToolCalls {
			if err := s.rec.Record(store.Record{
				Kind: store.KindToolCall,
				Ts:   store.NowISO(),
				ToolCall: &store.ToolCallRecord{
					ID:        tc.ID,
					Type:      tc.Type,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}); err != nil {
				s.l.Error("persist tool call", "session_id", s.sessionID, "error", err)
			}
		}
		if m.Role == "tool" {
			if err := s.rec.Record(store.Record{
				Kind: store.KindToolResult,
				Ts:   store.NowISO(),
				ToolResult: &store.ToolResultRecord{
					CallID: m.ToolCallID,
					Status: "success",
					Output: m.Text(),
				},
			}); err != nil {
				s.l.Error("persist tool result", "session_id", s.sessionID, "error", err)
			}
		}
	}
	s.persisted = len(s.history)
}

func (s *InfaiAgentSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.rec != nil {
		_ = s.rec.Close()
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
