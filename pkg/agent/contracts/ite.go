package contracts

import (
	"context"

	"github.com/google/uuid"
)

// Tool is an executable action the model may call, described for the system
// prompt.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

type ToolParameters struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	RequiredFields       []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

type ToolType string

const (
	ReadTool   ToolType = "read"
	WriteTool  ToolType = "write"
	EditTool   ToolType = "edit"
	GlobTool   ToolType = "glob"
	ListTool   ToolType = "list"
	SearchTool ToolType = "search"
	BashTool   ToolType = "bash"
)

type ToolExecutionStatus string

const (
	ToolExecutionSuccess ToolExecutionStatus = "success"
	ToolExecutionDenied  ToolExecutionStatus = "denied"
	ToolExecutionPending ToolExecutionStatus = "pending"
	ToolExecutionError   ToolExecutionStatus = "error"
)

type ToolExecutionResult struct {
	Status     ToolExecutionStatus
	ApprovalID string
	Output     string
	Error      string
}

// ToolExecutor is the boundary between an agent loop and its owner. The
// owner applies policy, executes the call, or waits for human approval.
type ToolExecutor interface {
	Execute(context.Context, uuid.UUID, ToolCall) ToolExecutionResult
}
