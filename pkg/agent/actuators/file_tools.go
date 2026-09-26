package actuators

import (
	"encoding/json"
	"errors"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func toolSchema(name, description string, properties map[string]any, required []string) contracts.Tool {
	return contracts.Tool{
		Name:        name,
		Description: description,
		Parameters: contracts.ToolParameters{
			Type:                 "object",
			Properties:           properties,
			RequiredFields:       required,
			AdditionalProperties: false,
		},
	}
}

// wrapToolError keeps the code, reason and responsibility of a filesystem error
// and falls back to a generic tool-side failure for anything else. Errors that
// already carry their tool context are returned untouched.
func wrapToolError(tool contracts.ToolType, err error, code, reason string) error {
	if _, ok := errors.AsType[*contracts.ExecutionError](err); ok {
		return err
	}
	if fileErr, ok := errors.AsType[*filesystemError](err); ok {
		return contracts.NewToolExecutionError(tool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
	}
	return contracts.NewToolExecutionError(tool, code, reason, contracts.ResponsibilityTool, err)
}

// Tool results are inserted directly into the next model request. Keep the
// serialized result small enough to leave room for instructions and history.
const (
	maxToolOutputBytes  = 32 << 10
	maxToolContentBytes = 24 << 10
)

func mustJSON(v any) ([]byte, error) {
	output, err := json.Marshal(v)
	if err != nil {
		return nil, filesystemErr("output_encoding_failed", "tool output could not be encoded", contracts.ResponsibilityTool, err)
	}
	if len(output) > maxToolOutputBytes {
		return nil, filesystemErr("output_too_large", "tool output is too large; narrow the path or pattern and request fewer results", contracts.ResponsibilityTool, nil)
	}
	return output, nil
}

func assemble(v any) (string, error) {
	output, err := mustJSON(v)
	if err != nil {
		return "", err
	}
	return string(output), nil
}
