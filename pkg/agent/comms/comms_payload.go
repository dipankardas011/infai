package comms

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type DelegationToSidecarLoop struct {
	ParentID uuid.UUID `json:"parent_id"`

	AgentName string `json:"agent_name"`
	Task      string `json:"task"`
	Cwd       string `json:"cwd,omitempty"`

	AcceptanceScript string `json:"acceptance_script,omitempty"`
	MaxTurns         uint64 `json:"max_turns,omitempty"`
}

type DelegationConformation struct {
	Err         string    `json:"error"`
	DelegatedTo uuid.UUID `json:"delegated_to"` // If there is a error we just have it default which is uuid.Nil
}

type DelegatedTaskResponse struct {
	From uuid.UUID `json:"from"`
	Name string    `json:"name,omitempty"`

	Status  contracts.SessionStatus `json:"status,omitempty"`
	Summary string                  `json:"summary,omitempty"`
	Error   string                  `json:"error,omitempty"`
}
