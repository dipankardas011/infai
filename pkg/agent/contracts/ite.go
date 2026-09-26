package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ToolCall is a function-call the model requested. Schema is defined now;
// the tool loop (AccessControl → execution → results) wires it later.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function names the tool and carries the JSON-encoded argument object.
type Function struct {
	Name      ToolType `json:"name"`
	Arguments string   `json:"arguments"`
}

func NewToolMessage(callID, content string, status ToolExecutionStatus) ChatMessage {
	return ChatMessage{Role: "tool", ToolCallID: callID, Content: &content, Status: status}
}

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
	ReadTool          ToolType = "read"
	WriteTool         ToolType = "write"
	EditTool          ToolType = "edit"
	GlobTool          ToolType = "glob"
	ListTool          ToolType = "list"
	SearchTool        ToolType = "search"
	BashTool          ToolType = "bash"
	ReadSkillTool     ToolType = "read_skill"
	TaskChecklistTool ToolType = "task_checklist"
)

func IsToolTypeSkill(t ToolType) bool {
	switch t {
	case ReadSkillTool:
		return true
	default:
		return false
	}
}

type ToolExecutionStatus string

const (
	ToolExecutionSuccess ToolExecutionStatus = "success"
	ToolExecutionDenied  ToolExecutionStatus = "denied"
	ToolExecutionPending ToolExecutionStatus = "pending"
	ToolExecutionError   ToolExecutionStatus = "error"
)

type ToolExecutionResult struct {
	Status   ToolExecutionStatus
	CallID   string
	CallName ToolType
	Output   string
	Error    string
}

// FailureResponsibility names who owns a tool failure: the model that asked for
// it, the session that dispatched it, the environment the tool touched, or the
// tool itself.
type FailureResponsibility string

const (
	ResponsibilityAgent       FailureResponsibility = "agent"
	ResponsibilitySession     FailureResponsibility = "session"
	ResponsibilityEnvironment FailureResponsibility = "environment"
	ResponsibilityTool        FailureResponsibility = "tool"
	ResponsibilityUser        FailureResponsibility = "user"
)

// ExecutionError is the failure shape every tool executor returns. Error is safe
// to send back to the model; the underlying cause is retained for logs only.
type ExecutionError struct {
	Tool           ToolType
	Code           string
	Reason         string
	Responsibility FailureResponsibility
	cause          error
}

// NewToolExecutionError is a tool failure the model is allowed to read.
func NewToolExecutionError(tool ToolType, code, reason string, responsibility FailureResponsibility, cause error) error {
	return &ExecutionError{
		Tool:           tool,
		Code:           code,
		Reason:         reason,
		Responsibility: responsibility,
		cause:          cause,
	}
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("tool %q failed (%s): %s; responsibility: %s", e.Tool, e.Code, e.Reason, e.Responsibility)
}

func (e *ExecutionError) Unwrap() error { return e.cause }

// DecodeToolArguments enforces the argument contract shared by every tool: one
// JSON object, no unknown fields, no trailing values.
func DecodeToolArguments[T any](tool ToolType, tc ToolCall) (T, error) {
	var args T

	raw := strings.TrimSpace(tc.Function.Arguments)
	if raw == "" || raw[0] != '{' {
		return args, NewToolExecutionError(tool, "invalid_arguments", "tool arguments must be a JSON object", ResponsibilityAgent, nil)
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, NewToolExecutionError(tool, "invalid_arguments", "tool arguments do not match the tool schema", ResponsibilityAgent, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return args, NewToolExecutionError(tool, "invalid_arguments", "tool arguments must contain one JSON value", ResponsibilityAgent, nil)
		}
		return args, NewToolExecutionError(tool, "invalid_arguments", "tool arguments contain trailing invalid JSON", ResponsibilityAgent, err)
	}
	return args, nil
}

// RunBounded releases the caller when a tool exceeds its deadline. Tool work
// cannot be interrupted once started — neither a syscall nor a locked in-memory
// mutation — so the worker finishes and discards its result. Only this tool's
// own deadline is reported as a timeout; an outer cancellation is passed through.
func RunBounded(sessionCtx context.Context, tool ToolType, timeout time.Duration, run func() (string, error)) (string, error) {
	timeoutCause := fmt.Errorf("%s exceeded the %s execution limit", tool, timeout)
	ctx, cancel := context.WithTimeoutCause(sessionCtx, timeout, timeoutCause)
	defer cancel()

	results := make(chan toolResult, 1)
	go func() {
		output, err := run()
		results <- toolResult{output: output, err: err}
	}()

	select {
	case <-ctx.Done():
		if cause := context.Cause(ctx); errors.Is(cause, timeoutCause) {
			return "", NewToolExecutionError(tool, "execution_timeout", fmt.Sprintf("the tool did not finish within %s", timeout), ResponsibilityTool, cause)
		}
		return "", ctx.Err()
	case result := <-results:
		return result.output, result.err
	}
}

type toolResult struct {
	output string
	err    error
}
