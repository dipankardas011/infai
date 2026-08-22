package server

import (
	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
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

type ChatRequest struct {
	Prompt string `json:"prompt"`
}

// ---- responses ----

type SessionDetailResponse struct {
	Meta    store.SessionMeta `json:"meta"`
	Records []store.Record    `json:"records"`
}

type ChatResponse struct {
	SessionID        uuid.UUID              `json:"session_id"`
	Status           string                 `json:"status"`
	Reply            string                 `json:"reply"`
	Model            string                 `json:"model"`
	ContextWindow    int                    `json:"ctx_window"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	Pending          *agent.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage  `json:"usage,omitempty"`
}

type ChatDeltaEvent struct {
	Kind  string `json:"kind"`
	Delta string `json:"delta"`
}

type ChatDoneEvent struct {
	Done             bool                   `json:"done"`
	SessionID        uuid.UUID              `json:"session_id"`
	Status           string                 `json:"status"`
	Reply            string                 `json:"reply"`
	ReasoningContent string                 `json:"reasoning_content"`
	Model            string                 `json:"model"`
	ContextWindow    int                    `json:"ctx_window"`
	Pending          *agent.ApprovalRequest `json:"pending,omitempty"`
	Usage            *contracts.TokenUsage  `json:"usage,omitempty"`
}

type ChatErrorEvent struct {
	Error string `json:"error"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
