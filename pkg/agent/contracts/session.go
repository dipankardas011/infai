package contracts

import (
	"time"

	"github.com/google/uuid"
)

// SessionSummary combines persisted identity with engine-owned runtime state
// for session-list API consumers.
type SessionSummary struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name,omitempty"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Cwd       string    `json:"cwd,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Active    bool      `json:"active"`
}
