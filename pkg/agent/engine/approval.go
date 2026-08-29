package engine

import (
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// ApprovalRequest is the external engine/client representation of a pending
// human decision. It deliberately contains no channel or runtime waiter.
type ApprovalRequest struct {
	ID        uuid.UUID          `json:"id"`
	SessionID uuid.UUID          `json:"session_id"`
	AgentID   uuid.UUID          `json:"agent_id"`
	ToolCall  contracts.ToolCall `json:"tool_call"`
	CreatedAt time.Time          `json:"created_at"`
}

type ApprovalDecision string

const (
	ApprovalApprove        ApprovalDecision = "approve"
	ApprovalDeny           ApprovalDecision = "deny"
	ApprovalDenyWithReason ApprovalDecision = "deny_with_reason"
)

type ApprovalDecisionRequest struct {
	Decision ApprovalDecision `json:"decision"`
	Reason   string           `json:"reason,omitempty"`
}
