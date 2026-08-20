package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

var ErrSessionClosed = errors.New("session closed")

// InfaiAgentSession is the persistent state of one conversation. It is a
// passive object: it holds history and the agent tree, and Chat() runs the
// loop against it. Between Chat calls the session is idle — no goroutine is
// held — so it stays registered until explicitly closed.
type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	model contracts.InfaiModelAdaptor

	mu      sync.Mutex
	closed  bool
	history []contracts.ChatMessage

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentComms        // By this when N no of children can send comms with engine and even the

	baseAgentId uuid.UUID

	Agents map[uuid.UUID]*agent.Agent
}

// model is chosen per-session (the model a session runs is a property of the
// conversation, not the engine). Hardcoded to a local OpenAI-compatible
// endpoint for now; the request payload will drive selection later.
func NewSession(l *slog.Logger) (*InfaiAgentSession, error) {
	o := &InfaiAgentSession{
		l:             l,
		model:         models.NewOpenAICompatableAPI("http://0.0.0.0:8000/v1", "local-model", ""),
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*agent.Agent),
	}

	if v, err := uuid.NewV7(); err != nil {
		return nil, err
	} else {
		o.sessionID = v
	}

	firstAgent, err := o.registerNewParentAgent()
	if err != nil {
		return nil, err
	}
	o.baseAgentId = firstAgent.Id

	return o, nil
}

func (s *InfaiAgentSession) ID() uuid.UUID {
	return s.sessionID
}

// Chat runs the base agent loop for one user prompt against the session's
// persistent history and returns the outcome. The session stays registered
// and idle after the call; the next Chat reuses the same conversation.
func (s *InfaiAgentSession) Chat(ctx context.Context, prompt string) (*ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrSessionClosed
	}

	if len(s.history) == 0 {
		sys, err := GetBasicSystemPrompt(nil, nil)
		if err != nil {
			return nil, err
		}
		s.history = append(s.history, contracts.NewSystemMessage(sys))
	}
	s.history = append(s.history, contracts.NewUserMessage(prompt))

	a := s.Agents[s.baseAgentId]
	if a == nil {
		return nil, errors.New("session: base agent missing")
	}

	result, err := a.Invoke(ctx, s.history)
	if err != nil {
		return nil, err
	}
	s.history = result.Messages

	return &ChatResult{
		SessionID:        s.sessionID,
		Status:           result.Status,
		Reply:            lastAssistantText(result.Messages),
		ReasoningContent: lastAssistantReasoning(result.Messages),
		Pending:          result.Pending,
		Usage:            result.Usage,
	}, nil
}

func (s *InfaiAgentSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
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
