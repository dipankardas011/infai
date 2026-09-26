package contracts

import (
	"fmt"
	"time"
)

type EventStreamKind string

const (
	// DeltaContent is the model's visible answer text.
	DeltaContent EventStreamKind = "content"
	// DeltaReasoning is the model's reasoning text (shown separately).
	DeltaReasoning EventStreamKind = "reasoning"
	// CompactionSummary is the summary a compaction produced. It is published
	// once the compacted history is installed, so it marks the session as
	// runnable again rather than mid-compaction.
	CompactionSummary EventStreamKind = "compaction_summary"

	// EventProviderEvent is the model provider speaking for itself: a retry
	// notice, a failed request, an endpoint-level message.
	EventProviderEvent EventStreamKind = "provider_event"

	// NotifyAgentUsage reports token usage for the last model request.
	NotifyAgentUsage EventStreamKind = "agent_usage"

	// Session-owned runtime notifications exposed to observers.
	EventSessionFatal  EventStreamKind = "session_fatal"
	EventSubscriberGap EventStreamKind = "subscriber_gap"

	// EventSessionTransitionState carries the session status in Content.
	// It is the only kind that moves the status, whether the transition comes
	// from the agent's own loop or from a session operation.
	EventSessionTransitionState EventStreamKind = "session_transition_state"

	/// ToolCall
	EventToolCall   EventStreamKind = "tool_call"
	EventToolResult EventStreamKind = "tool_result"

	/// TaskChecklist
	EventToolTaskCheckList EventStreamKind = "task_checklist_result"
	// SkillLoaded
	EventSkillLoad EventStreamKind = "skill_load"

	/// User Prompt
	EventMessageFromAgentInbox EventStreamKind = "message_from_agent_inbox"

	/// CompactionTriggered
	EventManualCompactionTriggered EventStreamKind = "session_manual_compaction"
	EventAutoCompactionTriggered   EventStreamKind = "session_auto_compaction"

	/// HITL
	EventApprovalRequested EventStreamKind = "approval_requested"
	EventApprovalResolved  EventStreamKind = "approval_resolved"
)

type EventStream struct {
	Kind        EventStreamKind      `json:"delta_kind,omitempty"`
	Timestamp   time.Time            `json:"ts"`
	Content     *string              `json:"content"`
	ToolCall    *ToolCall            `json:"tool_call"`
	ToolResult  *ToolExecutionResult `json:"tool_result"`
	HITLCall    *ApprovalRequest     `json:"hitl_call"`
	HITLResult  *ApprovalConclusion  `json:"hitl_result"`
	Attachments *EventAttachments    `json:"attachments,omitempty"`
}

type EventAttachments struct {
	ImageCount int `json:"image_count,omitempty"`
}

func ToolCallDisplay(call ToolCall) string {
	if call.Function.Arguments == "" {
		return string(call.Function.Name)
	}
	return fmt.Sprintf("%s %s", call.Function.Name, call.Function.Arguments)
}
