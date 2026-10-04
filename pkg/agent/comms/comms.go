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
	AgentCommDelegationConformation AgentCommKind = "delegation_conformation"

	AgentCommKindSpawnSidecar            AgentCommKind = "spawn_sidecar_loop"
	AgentCommKindSpawnSidecarBackground  AgentCommKind = "spawn_sidecar_loop_background"
	AgentCommKindResultSidecar           AgentCommKind = "result_sidecar_loop"
	AgentCommKindResultSidecarBackground AgentCommKind = "result_sidecar_loop_background"

	// AgentCommKindSidecarStatus is a delegated session reporting what it is
	// doing. It is fire-and-forget: the caller fans it out to its own clients.
	AgentCommKindSidecarStatus AgentCommKind = "sidecar_loop_status"

	AgentCommKindSwarm AgentCommKind = "swarm"
)

var allKinds = []AgentCommKind{
	AgentCommDelegationConformation,
	AgentCommKindSpawnSidecar,
	AgentCommKindSpawnSidecarBackground,
	AgentCommKindResultSidecar,
	AgentCommKindResultSidecarBackground,
	AgentCommKindSidecarStatus,
	AgentCommKindSwarm,
}

// AgentComm is one internal message between the session and an agent.
// ReplyFor correlates a response with its request — must be echoed by engine
// when responding; must be set by agent when it expects a reply.
type AgentComm struct {
	From uuid.UUID
	To   uuid.UUID // uuid.Nil means engine

	Kind     AgentCommKind
	ReplyFor *string // nil = fire and forget

	Payload json.RawMessage
}

var (
	ErrAgentCommsClosed   = errors.New("agent-engine communications closed")
	ErrAgentNotRegistered = errors.New("agent is not registered")
	ErrUnknownKind        = errors.New("unknown agent comm kind")
	ErrKindAlreadySubbed  = errors.New("already subscribed to this kind")
)

type agentInbox struct {
	kinds map[AgentCommKind]chan *AgentComm
	done  chan struct{}
}

// AgentComms is the session-owned communication hub.
// Agents have per-kind private inboxes; all agents share one engine inbox.
type AgentComms struct {
	mu               sync.RWMutex
	engineInbox      chan *AgentComm
	sessAgentInboxes map[uuid.UUID]agentInbox
	done             chan struct{}
	once             sync.Once
}

func NewAgentComms() *AgentComms {
	return &AgentComms{
		engineInbox:      make(chan *AgentComm, 64),
		sessAgentInboxes: make(map[uuid.UUID]agentInbox),
		done:             make(chan struct{}),
	}
}

func (c *AgentComms) RegisterSessionAgent(id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("agent id must not be nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.sessAgentInboxes[id]; exists {
		return errors.New("agent already registered")
	}

	inbox := agentInbox{kinds: make(map[AgentCommKind]chan *AgentComm, len(allKinds)), done: make(chan struct{})}
	for _, k := range allKinds {
		inbox.kinds[k] = make(chan *AgentComm, 8)
	}
	c.sessAgentInboxes[id] = inbox
	return nil
}

func (c *AgentComms) UnregisterSessionAgent(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	inbox, ok := c.sessAgentInboxes[id]
	if !ok {
		return
	}
	close(inbox.done)
	delete(c.sessAgentInboxes, id)
}

// SendToSessionAgent is used by the engine to route a message to a specific agent.
func (c *AgentComms) SendToSessionAgent(ctx context.Context, id uuid.UUID, msg *AgentComm) error {
	c.mu.RLock()
	inbox, ok := c.sessAgentInboxes[id]

	if !ok {
		c.mu.RUnlock()
		return ErrAgentNotRegistered
	}
	ch, ok := inbox.kinds[msg.Kind]
	if !ok {
		c.mu.RUnlock()
		return ErrUnknownKind
	}
	c.mu.RUnlock()

	return sendComm(ctx, ch, c.done, inbox.done, msg)
}

// SubscribeForSessionAgentsEvents is used by the engine to read all outbound agent messages.
func (c *AgentComms) SubscribeForSessionAgentsEvents(ctx context.Context) (*AgentComm, error) {
	return receiveComm(ctx, c.engineInbox, c.done)
}

func (c *AgentComms) Close() {
	c.once.Do(func() {
		close(c.done)
	})
}

// ISACChannel is the [I]nter[S]ession[A]gent[C]ommunication channel.
// One per agent. Agent uses this to talk to the engine and subscribe to replies.
type ISACChannel struct {
	agentId uuid.UUID
	c       *AgentComms

	mu   sync.Mutex
	subs map[AgentCommKind]context.CancelFunc
}

func (c *AgentComms) NewSessionAgentComms(id uuid.UUID) *ISACChannel {
	return &ISACChannel{
		agentId: id,
		c:       c,
		subs:    make(map[AgentCommKind]context.CancelFunc),
	}
}

// Send fires a message to the engine. Set ReplyFor if you expect a response.
func (iac *ISACChannel) Send(ctx context.Context, msg *AgentComm) error {
	iac.c.mu.RLock()
	inbox, ok := iac.c.sessAgentInboxes[iac.agentId]
	iac.c.mu.RUnlock()
	if !ok {
		return ErrAgentNotRegistered
	}
	return sendComm(ctx, iac.c.engineInbox, iac.c.done, inbox.done, msg)
}

// Subscribe registers a handler for a specific kind. Returns a closer — call it
// to unsubscribe. Handler must be non-blocking. One subscriber per kind at a time.
func (iac *ISACChannel) Subscribe(kind AgentCommKind, handler func(*AgentComm)) (func(), error) {
	iac.c.mu.RLock()
	inbox, ok := iac.c.sessAgentInboxes[iac.agentId]
	iac.c.mu.RUnlock()

	if !ok {
		return nil, ErrAgentNotRegistered
	}
	ch, ok := inbox.kinds[kind]
	if !ok {
		return nil, ErrUnknownKind
	}

	iac.mu.Lock()
	if _, alreadySubbed := iac.subs[kind]; alreadySubbed {
		iac.mu.Unlock()
		return nil, ErrKindAlreadySubbed
	}
	ctx, cancel := context.WithCancel(context.Background())
	iac.subs[kind] = cancel
	iac.mu.Unlock()

	go func() {
		for {
			select {
			case msg := <-ch:
				handler(msg)
			case <-ctx.Done():
				return
			case <-inbox.done:
				return
			case <-iac.c.done:
				return
			}
		}
	}()

	return func() {
		cancel()
		iac.mu.Lock()
		delete(iac.subs, kind)
		iac.mu.Unlock()
	}, nil
}

func sendComm(ctx context.Context, out chan *AgentComm, done <-chan struct{}, agentDone <-chan struct{}, msg *AgentComm) error {
	select {
	case out <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ErrAgentCommsClosed
	case <-agentDone:
		return ErrAgentNotRegistered
	}
}

func receiveComm(ctx context.Context, in <-chan *AgentComm, done <-chan struct{}) (*AgentComm, error) {
	select {
	case msg, open := <-in:
		if !open {
			return nil, ErrAgentCommsClosed
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		return nil, ErrAgentCommsClosed
	}
}
