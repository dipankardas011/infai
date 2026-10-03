package contracts

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type SessionStatus string

const (
	SessionIdle            SessionStatus = "idle"
	SessionBusy            SessionStatus = "busy"
	SessionWaitingApproval SessionStatus = "waiting_approval"
	SessionCompacting      SessionStatus = "compacting"

	// Concluded states: the session will not serve another turn.
	SessionCompleted             SessionStatus = "completed"
	SessionMaxIterationExhausted SessionStatus = "max_iteration_exhausted"
	SessionTombstone             SessionStatus = "tombstone"
)

// SessionSummary combines persisted identity with engine-owned runtime state
// for session-list API consumers.
type SessionSummary struct {
	ID        uuid.UUID     `json:"id"`
	ParentID  uuid.UUID     `json:"parent_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	Cwd       string        `json:"cwd,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Active    bool          `json:"active"`
	Status    SessionStatus `json:"status,omitempty"`
	AgentKind AgentKind     `json:"agent_kind,omitempty"`
}

type AgentKind string

const (
	InteractiveAgent AgentKind = "interactive"
	SidecarLoopAgent AgentKind = "sidecar_loop"
	SingleLoopAgent  AgentKind = "loop"
	SwarmAgent       AgentKind = "swarm"
)

type AgentMailbox struct {
	drainValve atomic.Bool
	fillValve  atomic.Bool
	mailbox    chan ChatMessage
}

func NewAgentMailboxForSession() *AgentMailbox {
	return &AgentMailbox{mailbox: make(chan ChatMessage, 10)}
}

func (am *AgentMailbox) IsEmpty() bool { return len(am.mailbox) == 0 }

// Purpose to avoid the agent to inject during manualCompaction duration. (but then why not have the same spinWait in the workingHistory of agent?)
func (am *AgentMailbox) PreventDraining() { am.drainValve.Store(false) }
func (am *AgentMailbox) AllowDraining()   { am.drainValve.Store(true) }

// for sidecar_loop as only one message
func (am *AgentMailbox) PreventFilling() { am.fillValve.Store(false) }
func (am *AgentMailbox) AllowFilling()   { am.fillValve.Store(true) }

func (am *AgentMailbox) SendMessage(ctx context.Context, message ChatMessage) error {
	if !am.fillValve.Load() {
		return fmt.Errorf("Adding message to agent mailbox")
	}

	select {
	case am.mailbox <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (am *AgentMailbox) spinWaiting(ctx context.Context) {
	for tc := time.NewTicker(time.Second); ; {
		select {
		case <-tc.C:
			if am.drainValve.Load() {
				tc.Stop()
				return
			}
		case <-ctx.Done():
			tc.Stop()
			return
		}

	}
}

func (am *AgentMailbox) ConsumeAllFromInbox(ctx context.Context) []ChatMessage {
	am.spinWaiting(ctx)

	var batch []ChatMessage

	for {
		select {
		case message := <-am.mailbox:
			batch = append(batch, message)
		default:
			return batch
		}
	}
}

func (am *AgentMailbox) ListenForMessageInInbox(ctx context.Context) <-chan ChatMessage {
	am.spinWaiting(ctx)
	return am.mailbox
}
