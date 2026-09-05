package actuators

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/runenames"

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

type filesystemError struct {
	code           string
	reason         string
	responsibility FailureResponsibility
	cause          error
}

func (e *filesystemError) Error() string { return e.reason }

func (e *filesystemError) Unwrap() error { return e.cause }

func filesystemErr(code, reason string, responsibility FailureResponsibility, cause error) error {
	return &filesystemError{
		code:           code,
		reason:         reason,
		responsibility: responsibility,
		cause:          cause,
	}
}

func validateText(value string, responsibility FailureResponsibility) error {
	if !utf8.ValidString(value) {
		return filesystemErr("invalid_utf8", "the value is not valid UTF-8", responsibility, nil)
	}
	position := 0
	for _, character := range value {
		if character == '\r' || character == '\n' || character == '\t' {
			position++
			continue
		}
		if isInvisible(character) {
			description := fmt.Sprintf("U+%04X", character)
			if name := runenames.Name(character); name != "" {
				description += " " + name
			}
			return filesystemErr(
				"invisible_character",
				fmt.Sprintf("disallowed invisible character %s at character %d", description, position),
				responsibility,
				nil,
			)
		}
		position++
	}
	return nil
}

func isInvisible(character rune) bool {
	return unicode.IsControl(character) || unicode.In(
		character,
		unicode.Cf,
		unicode.Properties["Other_Default_Ignorable_Code_Point"],
		unicode.Properties["Variation_Selector"],
		unicode.Properties["Noncharacter_Code_Point"],
	)
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("tool %q failed (%s): %s; responsibility: %s", e.Tool, e.Code, e.Reason, e.Responsibility)
}

func (e *ExecutionError) Unwrap() error { return e.cause }

func execErr(tool contracts.ToolType, code, reason string, responsibility FailureResponsibility, cause error) error {
	return &ExecutionError{Tool: string(tool), Code: code, Reason: reason, Responsibility: responsibility, cause: cause}
}

type contextKey uint8

const (
	toolCallKey contextKey = iota
)

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
	case contracts.WriteTool:
		output, err = writeExecution(toolContext)
	case contracts.EditTool:
		output, err = editExecution(toolContext)
	case contracts.ListTool:
		output, err = listExecution(toolContext)
	case contracts.GlobTool:
		output, err = globExecution(toolContext)
	case contracts.SearchTool:
		output, err = searchExecution(toolContext)
	case contracts.BashTool:
		output, err = bashExecution(toolContext)
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
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			err = execErr(contracts.ToolType(toolName), fileErr.code, fileErr.reason, fileErr.responsibility, err)
		} else if executionErr, ok := errors.AsType[*ExecutionError](err); ok {
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
