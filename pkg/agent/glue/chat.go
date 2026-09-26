package glue

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type ChatRequest struct {
	Prompt   string                       `json:"prompt"`
	Images   []contracts.ImageInput       `json:"images,omitempty"`
	Thinking contracts.InfaiThinkingLevel `json:"thinking"`
}

type ChatResponse struct {
	SessionID        uuid.UUID                  `json:"session_id"`
	Status           string                     `json:"status"`
	Reply            string                     `json:"reply"`
	Model            string                     `json:"model"`
	Name             string                     `json:"name,omitempty"`
	ContextWindow    uint64                     `json:"ctx_window"`
	ReasoningContent string                     `json:"reasoning_content,omitempty"`
	Pending          *contracts.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage      `json:"usage,omitempty"`
	ContextTokens    uint64                     `json:"context_tokens"`
}

type ChatDeltaEvent struct {
	Kind  string `json:"kind"`
	Delta string `json:"delta"`
}

type ApprovalSSEEvent struct {
	Type        string              `json:"type"`
	ID          uuid.UUID           `json:"id"`
	SessionID   uuid.UUID           `json:"session_id"`
	AgentID     uuid.UUID           `json:"agent_id"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	ToolCall    *contracts.ToolCall `json:"tool_call,omitempty"`
	Decision    string              `json:"decision,omitempty"`
	Reason      string              `json:"reason,omitempty"`
}

type ChatDoneEvent struct {
	Done             bool                       `json:"done"`
	SessionID        uuid.UUID                  `json:"session_id"`
	Status           string                     `json:"status"`
	Reply            string                     `json:"reply"`
	ReasoningContent string                     `json:"reasoning_content"`
	Model            string                     `json:"model"`
	Name             string                     `json:"name,omitempty"`
	ContextWindow    uint64                     `json:"ctx_window"`
	Pending          *contracts.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage      `json:"usage,omitempty"`
	ContextTokens    uint64                     `json:"context_tokens"`
}

type ChatErrorEvent struct {
	Error string `json:"error"`
}
