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
	// DeltaStatus is a live UI status update, not model output.
	DeltaStatus EventStreamKind = "status"
	// DeltaCompactionSummary is a live-only compaction summary for the UI.
	DeltaCompactionSummary EventStreamKind = "compaction_summary"
	// DeltaToolCall identifies a tool invocation requested by the model.
	DeltaToolCall EventStreamKind = "tool_call"
	// DeltaToolResult identifies the completion of a tool invocation.
	DeltaToolResult EventStreamKind = "tool_result"
	// DeltaSkillLoad identifies a skill being loaded from memory into context.
	DeltaSkillLoad EventStreamKind = "skill_load"
	// DeltaTaskChecklist carries the current structured task checklist state.
	DeltaTaskChecklist EventStreamKind = "task_checklist"
	// DeltaUserPrompt
	DeltaUserPrompt EventStreamKind = "user_prompt"

	//// Notify Session about Agent

	NotifyAgentModelError          EventStreamKind = "agent_model_err"
	NotifyAgentReachedMaxQ         EventStreamKind = "agent_at_max_q"
	NotifyAgentUsage               EventStreamKind = "agent_usage"
	NotifyAgentNeedsAutoCompaction EventStreamKind = "agent_needs_auto_compat"
	NotifyAgentMissingHistory      EventStreamKind = "agent_missing_history"
	NotifyAgentSessionStatus       EventStreamKind = "agent_session_status"

	/// ToolCall
	EventToolCall   EventStreamKind = "tool_call"
	EventToolResult EventStreamKind = "tool_result"

	/// HITL
	EventApprovalRequested EventStreamKind = "approval_requested"
	EventApprovalResolved  EventStreamKind = "approval_resolved"
	EventApprovalCanceled  EventStreamKind = "approval_canceled"
)

type EventStream struct {
	Kind       EventStreamKind      `json:"delta_kind,omitempty"`
	Timestamp  time.Time            `json:"ts"`
	Content    *string              `json:"content"`
	ToolCall   *ToolCall            `json:"tool_call"`
	ToolResult *ToolExecutionResult `json:"tool_result"`
	HITLCall   *ApprovalRequest     `json:"hitl_call"`
	HITLResult *ApprovalConclusion  `json:"hitl_result"`
}

func ToolCallDisplay(call ToolCall) string {
	if call.Function.Arguments == "" {
		return string(call.Function.Name)
	}
	return fmt.Sprintf("%s %s", call.Function.Name, call.Function.Arguments)
}

type AgentStatus string

const (
	AgentIdle      AgentStatus = "idle"
	AgentBusy      AgentStatus = "busy"
	AgentClosed    AgentStatus = "closed"
	AgentCompleted AgentStatus = "completed"
)
