package contracts

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ElicitationRequest is the external engine/client representation of a question
// an MCP server asked mid-call. It deliberately contains no channel or runtime
// waiter.
type ElicitationRequest struct {
	ID          uuid.UUID       `json:"id"`
	SessionID   uuid.UUID       `json:"session_id"`
	SessionName string          `json:"session_name"`
	Server      string          `json:"server"`
	Mode        string          `json:"mode"`
	Message     string          `json:"message"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

const (
	ElicitationAccept  = "accept"
	ElicitationDecline = "decline"
	ElicitationCancel  = "cancel"
)

type ElicitationConclusion struct {
	ReqID   uuid.UUID      `json:"req_id"`
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}
