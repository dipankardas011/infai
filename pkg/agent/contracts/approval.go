package contracts

import (
	"time"

	"github.com/google/uuid"
)

// ApprovalRequest is the external engine/client representation of a pending
// human decision. It deliberately contains no channel or runtime waiter.
type ApprovalRequest struct {
	ID          uuid.UUID `json:"id"`
	SessionID   uuid.UUID `json:"session_id"`
	SessionName string    `json:"session_name"`
	ToolCall    ToolCall  `json:"tool_call"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

type ApprovalDecision string

const (
	ApprovalApprove        ApprovalDecision = "approve"
	ApprovalDeny           ApprovalDecision = "deny"
	ApprovalDenyWithReason ApprovalDecision = "deny_with_reason"
)

type ApprovalConclusion struct {
	ReqID       uuid.UUID        `json:"req_id"`
	Fingerprint string           `json:"fingerprint"`
	Decision    ApprovalDecision `json:"decision"`
	Reason      string           `json:"reason,omitempty"`
}
