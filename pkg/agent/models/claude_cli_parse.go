package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// claudeTools is the set offered to the model, keyed by tool name. The value
// carries the tool's own parameter schema, which is also the validator the
// provider uses to repair Claude's argument types.
type claudeTools map[string]contracts.Tool

// claudeToolSchema builds the JSON Schema that tells the CLI what shape to
// answer in. Tools are represented as data inside it, so the model requests
// them structurally and the CLI never runs anything.
//
// Every tool becomes one variant of tool_calls items, keyed by a const name
// with the tool's own parameter schema as its arguments. With no tools the
// array is pinned to empty, which keeps a plain answer answerable.
func claudeToolSchema(tools []contracts.Tool) (string, claudeTools, error) {
	ordered := append([]contracts.Tool(nil), tools...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	allowed := make(claudeTools, len(ordered))
	variants := make([]map[string]any, 0, len(ordered))
	for _, tool := range ordered {
		if _, exists := allowed[tool.Name]; exists {
			return "", nil, fmt.Errorf("claude cli: two tools are named %q", tool.Name)
		}
		allowed[tool.Name] = tool
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"const": tool.Name},
				"arguments": claudeArgumentsSchema(tool),
			},
			"required":             []string{"name", "arguments"},
			"additionalProperties": false,
		})
	}

	items := any(false)
	if len(variants) > 0 {
		items = map[string]any{"oneOf": variants}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content":    map[string]any{"type": []string{"string", "null"}},
			"tool_calls": map[string]any{"type": "array", "items": items, "maxItems": len(variants)},
		},
		"required":             []string{"content", "tool_calls"},
		"additionalProperties": false,
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", nil, fmt.Errorf("claude cli: serialize output schema: %w", err)
	}
	return string(raw), allowed, nil
}

// claudeArgumentsSchema reuses the tool's declared parameter schema verbatim,
// falling back to an open object for a tool that declares none.
func claudeArgumentsSchema(tool contracts.Tool) any {
	if len(tool.Parameters) == 0 {
		return map[string]any{"type": "object"}
	}
	var schema any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		return map[string]any{"type": "object"}
	}
	return schema
}

// claudeStructuredOutput is the validated object the CLI hands back.
type claudeStructuredOutput struct {
	Content   *string                    `json:"content"`
	ToolCalls []claudeStructuredToolCall `json:"tool_calls"`
}

type claudeStructuredToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// claudeMessage converts the validated object into infai's assistant message,
// rejecting anything the schema should have prevented. A name the CLI invented
// is an error rather than a tool call: infai only executes tools it offered.
//
// A native call replaces the structured object: the reader ends the turn as
// soon as it sees one, so there is no result event to read.
func claudeMessage(result claudeStreamResult, allowed claudeTools) (contracts.ChatMessage, error) {
	if result.NativeTool != nil {
		message := contracts.ChatMessage{Role: "assistant", ReasoningContent: result.Reasoning}
		if result.NativeText != "" {
			message.Content = &result.NativeText
		}
		call, err := claudeToolCall(result.NativeTool.Name, result.NativeTool.Input, allowed)
		if err != nil {
			return contracts.ChatMessage{}, err
		}
		message.ToolCalls = append(message.ToolCalls, call)
		return message, nil
	}
	output := result.Output
	if output == nil {
		return contracts.ChatMessage{}, errors.New("claude cli: the CLI returned no structured output")
	}
	message := contracts.ChatMessage{Role: "assistant", ReasoningContent: result.Reasoning}
	if output.Content != nil && *output.Content != "" {
		message.Content = output.Content
	}
	for _, call := range output.ToolCalls {
		converted, err := claudeToolCall(call.Name, call.Arguments, allowed)
		if err != nil {
			return contracts.ChatMessage{}, err
		}
		message.ToolCalls = append(message.ToolCalls, converted)
	}
	return message, nil
}

// claudeToolCall validates one requested call and converts it to infai's shape.
// Names and argument objects are checked identically whether the request came
// through the output schema or the CLI's native channel. The tool's own schema
// repairs Claude's type mistakes before infai's strict decoder sees the object.
func claudeToolCall(name string, arguments json.RawMessage, allowed claudeTools) (contracts.ToolCall, error) {
	tool, ok := allowed[name]
	if !ok {
		return contracts.ToolCall{}, fmt.Errorf("claude cli: the CLI asked for tool %q, which was not offered", name)
	}
	var parsed map[string]any
	if len(arguments) == 0 || json.Unmarshal(arguments, &parsed) != nil || parsed == nil {
		return contracts.ToolCall{}, fmt.Errorf("claude cli: tool %q was given arguments that are not a JSON object", name)
	}
	compact, err := json.Marshal(claudeArguments(tool, parsed))
	if err != nil {
		return contracts.ToolCall{}, fmt.Errorf("claude cli: tool %q arguments: %w", name, err)
	}
	return contracts.ToolCall{
		ID:   claudeToolCallID(),
		Type: "function",
		Function: contracts.Function{
			Name:      contracts.ToolType(name),
			Arguments: string(compact),
		},
	}, nil
}
