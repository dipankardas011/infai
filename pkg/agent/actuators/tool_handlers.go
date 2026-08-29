package actuators

import (
	"context"
	"errors"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type FailureResponsibility string

const (
	ResponsibilityAgent       FailureResponsibility = "agent"
	ResponsibilitySession     FailureResponsibility = "session"
	ResponsibilityEnvironment FailureResponsibility = "environment"
	ResponsibilityTool        FailureResponsibility = "tool"
)

// ExecutionError is safe to send back to the model. The underlying cause is
// retained for logging, but Error never exposes it.
type ExecutionError struct {
	Tool           string
	Code           string
	Reason         string
	Responsibility FailureResponsibility
	cause          error
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("tool %q failed (%s): %s; responsibility: %s", e.Tool, e.Code, e.Reason, e.Responsibility)
}

func (e *ExecutionError) Unwrap() error { return e.cause }

type Workspace struct{ Root string }

type contextKey uint8

const (
	workspaceKey contextKey = iota
	toolCallKey
)

func WithWorkspace(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, workspaceKey, Workspace{Root: root})
}

func WorkspaceFromContext(ctx context.Context) (Workspace, bool) {
	workspace, ok := ctx.Value(workspaceKey).(Workspace)
	return workspace, ok && workspace.Root != ""
}

func WithToolCall(ctx context.Context, call contracts.ToolCall) context.Context {
	return context.WithValue(ctx, toolCallKey, call)
}

func ToolCallFromContext(ctx context.Context) (contracts.ToolCall, bool) {
	call, ok := ctx.Value(toolCallKey).(contracts.ToolCall)
	return call, ok
}

func ExecuteToolCall(ctx context.Context, call contracts.ToolCall) (output string, err error) {
	toolName := call.Function.Name
	toolContext := WithToolCall(ctx, call)

	switch contracts.ToolType(toolName) {
	case contracts.ReadTool:
		output, err = readExecution(toolContext)
	default:
		err = &ExecutionError{
			Tool:           toolName,
			Code:           "unknown_tool",
			Reason:         "the requested tool is not available in this session",
			Responsibility: ResponsibilityAgent,
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	if err != nil {
		if executionErr, ok := errors.AsType[*ExecutionError](err); ok {
			err = executionErr
		} else if ctx.Err() == nil {
			err = &ExecutionError{
				Tool:           toolName,
				Code:           "execution_failed",
				Reason:         "the tool could not complete the requested operation",
				Responsibility: ResponsibilityTool,
				cause:          err,
			}
		}
		output = ""
	}
	return output, err
}
