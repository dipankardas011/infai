package comms

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/uuid"
)

type AgentCommKind string

const (
	AgentCommMessage  AgentCommKind = "message"
	AgentCommTool     AgentCommKind = "tool"
	AgentCommSubagent AgentCommKind = "subagent"

	// AgentCommResult AgentCommKind = "result"
	// AgentCommError  AgentCommKind = "error"
	// AgentCommCancel AgentCommKind = "cancel"
)

// AgentComm is one internal message between the session and an agent. ID
// identifies this message; ReplyTo correlates a response with its request.
// These fields are for agent routing and are distinct from a model ToolCall ID.
type AgentComm struct {
	ID      uuid.UUID
	ReplyTo uuid.UUID
	From    uuid.UUID
	To      uuid.UUID
	Kind    AgentCommKind
	Payload json.RawMessage
}

var (
	ErrAgentCommsClosed   = errors.New("agent communications closed")
	ErrAgentNotRegistered = errors.New("agent is not registered")
)

// AgentComms is the session-owned communication hub. Agents have private
// inboxes, while every agent sends outbound messages to one session inbox.
type AgentComms struct {
	mu           sync.RWMutex
	sessionInbox chan AgentComm
	agentInboxes map[uuid.UUID]chan AgentComm
	done         chan struct{}
	once         sync.Once
}

func NewAgentComms() *AgentComms {
	return &AgentComms{
		sessionInbox: make(chan AgentComm),
		agentInboxes: make(map[uuid.UUID]chan AgentComm),
		done:         make(chan struct{}),
	}
}

func (c *AgentComms) RegisterAgent(id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("agent id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.agentInboxes[id]; exists {
		return errors.New("agent is already registered")
	}
	c.agentInboxes[id] = make(chan AgentComm)
	return nil
}

func (c *AgentComms) UnregisterAgent(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.agentInboxes, id)
}

func (c *AgentComms) SendToAgent(ctx context.Context, id uuid.UUID, msg AgentComm) error {
	c.mu.RLock()
	inbox, ok := c.agentInboxes[id]
	c.mu.RUnlock()
	if !ok {
		return ErrAgentNotRegistered
	}
	return sendComm(ctx, inbox, c.done, msg)
}

func (c *AgentComms) ReceiveFromAgents(ctx context.Context) (AgentComm, error) {
	return receiveComm(ctx, c.sessionInbox, c.done)
}

type IACChannel struct {
	agentId uuid.UUID
	c       *AgentComms
}

// IACChannel is Inter Agent Communication Channel only used for Agent to talk with session/engine.
func (c *AgentComms) IACChannel(id uuid.UUID) *IACChannel {
	return &IACChannel{id, c}
}

func (iac *IACChannel) Send(ctx context.Context, msg AgentComm) error {
	return sendComm(ctx, iac.c.sessionInbox, iac.c.done, msg)
}

func (iac *IACChannel) Receive(ctx context.Context) (AgentComm, error) {
	return iac.c.receiveForAgent(ctx, iac.agentId)
}

func (c *AgentComms) receiveForAgent(ctx context.Context, id uuid.UUID) (AgentComm, error) {
	c.mu.RLock()
	inbox, ok := c.agentInboxes[id]
	c.mu.RUnlock()
	if !ok {
		return AgentComm{}, ErrAgentNotRegistered
	}
	return receiveComm(ctx, inbox, c.done)
}

func (c *AgentComms) Close() {
	c.once.Do(func() {
		close(c.done)
	})
}

func sendComm(ctx context.Context, out chan AgentComm, done <-chan struct{}, msg AgentComm) error {
	select {
	case out <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ErrAgentCommsClosed
	}
}

func receiveComm(ctx context.Context, in <-chan AgentComm, done <-chan struct{}) (AgentComm, error) {
	select {
	case msg := <-in:
		return msg, nil
	case <-ctx.Done():
		return AgentComm{}, ctx.Err()
	case <-done:
		return AgentComm{}, ErrAgentCommsClosed
	}
}
