package actuators

import (
	"context"
	"encoding/json"
	"io"
	"strings"

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

func decodeArgs(ctx context.Context, dst any) (contracts.ToolCall, error) {
	c, ok := ToolCallFromContext(ctx)
	if !ok {
		return c, filesystemErr("missing_tool_call", "the tool request is missing its arguments", ResponsibilitySession, nil)
	}
	d := json.NewDecoder(strings.NewReader(c.Function.Arguments))
	d.DisallowUnknownFields()
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return c, filesystemErr("invalid_arguments", "tool arguments are not valid JSON", ResponsibilityAgent, err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		return c, filesystemErr("invalid_arguments", "tool arguments must be a JSON object", ResponsibilityAgent, nil)
	}
	objectDecoder := json.NewDecoder(strings.NewReader(string(raw)))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(dst); err != nil {
		return c, filesystemErr("invalid_arguments", "tool arguments do not match the tool schema", ResponsibilityAgent, err)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		if err == nil {
			return c, filesystemErr("invalid_arguments", "tool arguments must contain one JSON value", ResponsibilityAgent, nil)
		}
		return c, filesystemErr("invalid_arguments", "tool arguments contain trailing invalid JSON", ResponsibilityAgent, err)
	}
	return c, nil
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
		return nil, filesystemErr("output_encoding_failed", "tool output could not be encoded", ResponsibilityTool, err)
	}
	if len(output) > maxToolOutputBytes {
		return nil, filesystemErr("output_too_large", "tool output is too large; narrow the path or pattern and request fewer results", ResponsibilityTool, nil)
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
