package agent

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type TurnStatus int

const (
	// TurnDone is a normal completion: the loop exhausted MaxTurns or had
	// nothing left to do.
	TurnDone TurnStatus = iota
	// TurnCanceled means the session context was canceled mid-run (client
	// closed the session or shutdown).
	TurnCanceled
	// TurnPendingApproval means the loop paused at a human-in-the-loop
	// checkpoint and must be resumed with an approval decision.
	TurnPendingApproval
)

func (s TurnStatus) String() string {
	switch s {
	case TurnCanceled:
		return "canceled"
	case TurnPendingApproval:
		return "pending_approval"
	default:
		return "done"
	}
}

// ApprovalRequest is a human-in-the-loop checkpoint the agent reached. The
// session must be resumed with a decision before the loop continues.
type ApprovalRequest struct {
	Id      uuid.UUID
	Message string
}

// TurnResult is the outcome of one Invoke. Distinguish normal completion from
// cancellation so the caller can decide whether the session stays open.
type TurnResult struct {
	Status   TurnStatus
	Messages []contracts.ChatMessage
	Pending  *ApprovalRequest
	Usage    *contracts.TokenUsage
}

// CompactionResult carries a continuation summary produced by the session's
// separate compaction request.
type CompactionResult struct {
	Summary string
}
