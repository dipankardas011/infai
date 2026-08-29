package agent

import "github.com/dipankardas011/infai/pkg/agent/contracts"

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

// TurnResult is the outcome of one Invoke. Distinguish normal completion from
// cancellation so the caller can decide whether the session stays open.
type TurnResult struct {
	Status   TurnStatus
	Messages []contracts.ChatMessage
	Usage    *contracts.TokenUsage

	// PendingApprovalID identifies an approval owned by the engine/session.
	// The agent does not own the external approval request or its transport.
	PendingApprovalID string
}

// CompactionResult carries a continuation summary produced by the session's
// separate compaction request.
type CompactionResult struct {
	Summary string
}
