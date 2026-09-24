package glue

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

type SessionView struct {
	Meta            store.SessionMeta          `json:"meta"`
	History         []contracts.ChatMessage    `json:"history"`
	Status          contracts.SessionStatus    `json:"status"`
	InFlight        []contracts.EventStream    `json:"in_flight"`
	PendingApproval *contracts.ApprovalRequest `json:"pending_approval,omitempty"`
}

// SessionOutput combines durable session metadata with runtime model capacity.
type SessionOutput struct {
	store.SessionMeta
	ContextWindow     uint64                           `json:"context_window"`
	Thinking          contracts.InfaiThinkingLevel     `json:"thinking"`
	AvailableThinking []contracts.InfaiThinkingLevel   `json:"available_thinking"`
	Modalities        []contracts.LLMSupportedModality `json:"modalities"`
}

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

type BranchRequest struct {
	EventID uuid.UUID `json:"event_id"`
}

type SessionDetailResponse struct {
	Meta    store.SessionMeta `json:"meta"`
	Records []store.Record    `json:"records"`
}

type TimelineEventResponse struct {
	ID         uuid.UUID        `json:"id"`
	ParentID   uuid.UUID        `json:"parent_id"`
	BranchFrom *uuid.UUID       `json:"branch_from,omitempty"`
	Kind       store.RecordKind `json:"kind"`
	Record     *store.Record    `json:"record,omitempty"`

	// When Record is TOO Big or Images are there then we use these both
	BlobHash string              `json:"blob_hash,omitempty"`
	Preview  *store.EventPreview `json:"preview,omitempty"`
}

type TimelineResponse struct {
	Meta   store.SessionMeta       `json:"meta"`
	Head   uuid.UUID               `json:"head"`
	Events []TimelineEventResponse `json:"events"`
}
