package engine

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/google/uuid"
)

type AgentComm struct {
	From uuid.UUID
	To   uuid.UUID
	Msg  []byte
}

type AgentComms struct {
	R <-chan AgentComm
	W chan<- AgentComm
}

func FreshAgentComms() *AgentComms {
	return &AgentComms{
		R: make(chan AgentComm),
		W: make(chan AgentComm),
	}
}

// ChatResult is the outcome of one Chat call on a session.
type ChatResult struct {
	SessionID uuid.UUID
	Status    agent.TurnStatus
	Reply     string
	Pending   *agent.ApprovalRequest
}

// SessionInfo is a registered session, for listing.
type SessionInfo struct {
	Id uuid.UUID
}

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrEngineShuttingDown = errors.New("engine is shutting down")
)

// InfaiAgentEngine owns the session registry. Sessions are created, stay
// registered in an idle state (no goroutine held), and are reused by Chat
// until CloseSession or Shutdown.
type InfaiAgentEngine struct {
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig

	_ *sql.DB

	mu       sync.Mutex
	sessions map[uuid.UUID]*InfaiAgentSession

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	o := &InfaiAgentEngine{
		bgLogger:  bgLogger,
		engineCfg: cfg,
		sessions:  make(map[uuid.UUID]*InfaiAgentSession),
		stopCh:    make(chan struct{}),
	}

	return o, nil
}

// CreateSession registers a new idle session. It persists until CloseSession
// or Shutdown; a prompt can be sent any number of times via Chat.
func (e *InfaiAgentEngine) CreateSession(ctx context.Context) (*InfaiAgentSession, error) {
	select {
	case <-e.stopCh:
		return nil, ErrEngineShuttingDown
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	sess, err := NewSession(e.bgLogger.WithGroup("session"))
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.sessions[sess.sessionID] = sess
	e.mu.Unlock()

	e.bgLogger.Info("session created", "session_id", sess.sessionID)
	return sess, nil
}

// Chat runs one prompt against an existing session and returns the outcome.
func (e *InfaiAgentEngine) Chat(ctx context.Context, id uuid.UUID, prompt string) (*ChatResult, error) {
	e.mu.Lock()
	sess, ok := e.sessions[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess.Chat(ctx, prompt)
}

// CloseSession removes and closes a session. An in-flight Chat finishes or is
// canceled by its own context.
func (e *InfaiAgentEngine) CloseSession(id uuid.UUID) error {
	e.mu.Lock()
	sess, ok := e.sessions[id]
	if ok {
		delete(e.sessions, id)
	}
	e.mu.Unlock()

	if !ok {
		return ErrSessionNotFound
	}
	sess.close()
	e.bgLogger.Info("session closed", "session_id", id)
	return nil
}

// ListSessions returns the registered (idle or running) sessions.
func (e *InfaiAgentEngine) ListSessions() []SessionInfo {
	e.mu.Lock()
	defer e.mu.Unlock()

	infos := make([]SessionInfo, 0, len(e.sessions))
	for id := range e.sessions {
		infos = append(infos, SessionInfo{Id: id})
	}
	return infos
}

// Shutdown stops accepting sessions and closes every registered one.
func (e *InfaiAgentEngine) Shutdown(ctx context.Context) error {
	e.bgLogger.DebugContext(ctx, "received shutdown request")

	e.stopOnce.Do(func() {
		close(e.stopCh)
	})

	e.mu.Lock()
	for id, sess := range e.sessions {
		sess.close()
		delete(e.sessions, id)
	}
	e.mu.Unlock()

	e.bgLogger.DebugContext(ctx, "engine shutdown complete")
	return nil
}
