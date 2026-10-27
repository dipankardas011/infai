package glue

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

type SessionView struct {
	Meta               store.SessionMeta             `json:"meta"`
	History            []contracts.ChatMessage       `json:"history"`
	Status             contracts.SessionStatus       `json:"status"`
	InFlight           []contracts.EventStream       `json:"in_flight"`
	Checklist          contracts.TaskChecklistState  `json:"checklist"`
	MCP                []contracts.MCPServerStatus   `json:"mcp,omitempty"`
	Prompts            []contracts.MCPPrompt         `json:"prompts,omitempty"`
	Resources          []contracts.MCPResource       `json:"resources,omitempty"`
	PendingApproval    *contracts.ApprovalRequest    `json:"pending_approval,omitempty"`
	PendingElicitation *contracts.ElicitationRequest `json:"pending_elicitation,omitempty"`
	ActivePolicy       string                        `json:"active_policy,omitempty"`
	AvailablePolicies  []string                      `json:"available_policies,omitempty"`
}

// SessionOutput combines durable session metadata with runtime model capacity.
type SessionOutput struct {
	store.SessionMeta
	ContextWindow     uint64                           `json:"context_window"`
	Thinking          contracts.InfaiThinkingLevel     `json:"thinking"`
	AvailableThinking []contracts.InfaiThinkingLevel   `json:"available_thinking"`
	InputModalities   []contracts.LLMSupportedModality `json:"input_modalities"`
	OutputModalities  []contracts.LLMSupportedModality `json:"output_modalities"`
	Tools             []contracts.Tool                 `json:"tools,omitempty"`
	Skills            []contracts.Skill                `json:"skills,omitempty"`

	ActivePolicy      string   `json:"active_policy,omitempty"`
	AvailablePolicies []string `json:"available_policies,omitempty"`

	MCP []contracts.MCPServerStatus `json:"mcp,omitempty"`
}

// HITLPolicyView is one named tool policy and the decisions it resolves to.
type HITLPolicyView struct {
	Name      string            `json:"name"`
	Decisions map[string]string `json:"decisions"`
}

// HITLPoliciesOutput is the engine's policy set: the one new sessions start on,
// and every policy it can switch to.
type HITLPoliciesOutput struct {
	Policies []HITLPolicyView `json:"policies"`
}

type CreateHITLPolicyRequest struct {
	Name   string                     `json:"name"`
	Policy contracts.HITLPolicyConfig `json:"policy"`
}

type SetHITLPolicyRequest struct {
	Policy string `json:"policy"`
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

type RenderPromptRequest struct {
	Server    string            `json:"server"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

type ResolveElicitationRequest struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

type BranchRequest struct {
	EventID uuid.UUID `json:"event_id"`
}

type SessionIdentityResponse struct {
	Name      string              `json:"name"`
	AgentKind contracts.AgentKind `json:"agent_kind"`
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
