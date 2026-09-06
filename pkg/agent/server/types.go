package server

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/engine"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// ---- requests ----

type CreateSessionRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Cwd      string `json:"cwd"`
}

type SetSessionModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type RenameSessionRequest struct {
	Name string `json:"name"`
}

type ChatRequest struct {
	Prompt string `json:"prompt"`
}

type BranchRequest struct {
	EventID uuid.UUID `json:"event_id"`
}

// ---- responses ----

type SessionDetailResponse struct {
	Meta    store.SessionMeta `json:"meta"`
	Records []store.Record    `json:"records"`
}

type TimelineEventResponse struct {
	ID         uuid.UUID        `json:"id"`
	ParentID   uuid.UUID        `json:"parent_id"`
	BranchFrom *uuid.UUID       `json:"branch_from,omitempty"`
	Kind       store.RecordKind `json:"kind"`
	BlobHash   string           `json:"blob_hash,omitempty"`
	Record     *store.Record    `json:"record,omitempty"`
}

type TimelineResponse struct {
	Meta   store.SessionMeta       `json:"meta"`
	Head   uuid.UUID               `json:"head"`
	Events []TimelineEventResponse `json:"events"`
}

type ChatResponse struct {
	SessionID        uuid.UUID               `json:"session_id"`
	Status           string                  `json:"status"`
	Reply            string                  `json:"reply"`
	Model            string                  `json:"model"`
	Name             string                  `json:"name,omitempty"`
	ContextWindow    int                     `json:"ctx_window"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"`
	Pending          *engine.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage   `json:"usage,omitempty"`
	ContextTokens    int                     `json:"context_tokens"`
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
	Done             bool                    `json:"done"`
	SessionID        uuid.UUID               `json:"session_id"`
	Status           string                  `json:"status"`
	Reply            string                  `json:"reply"`
	ReasoningContent string                  `json:"reasoning_content"`
	Model            string                  `json:"model"`
	Name             string                  `json:"name,omitempty"`
	ContextWindow    int                     `json:"ctx_window"`
	Pending          *engine.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage   `json:"usage,omitempty"`
	ContextTokens    int                     `json:"context_tokens"`
}

type ChatErrorEvent struct {
	Error string `json:"error"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
