package contracts

import (
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
	Name      string        `json:"name,omitempty"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	Cwd       string        `json:"cwd,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Active    bool          `json:"active"`
	Status    SessionStatus `json:"status,omitempty"`
}

type AgentKind string

const (
	InteractiveAgent AgentKind = "interactive"
	SingleLoopAgent  AgentKind = "loop"
	SwarmAgent       AgentKind = "swarm"
)
