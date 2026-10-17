package models

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/delegate"
)

func TestClaudeArgumentsRepairsDeclaredTypes(t *testing.T) {
	nested := contracts.Tool{Name: "nested", Parameters: contracts.ToolParameterObjectSchema(map[string]any{
		"items": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":       "object",
				"properties": map[string]any{"count": map[string]any{"type": "integer"}},
			},
		},
		"meta": map[string]any{
			"type":       "object",
			"properties": map[string]any{"enabled": map[string]any{"type": "boolean"}},
		},
	}, nil)}

	cases := []struct {
		name string
		tool contracts.Tool
		in   map[string]any
		want map[string]any
	}{
		{
			name: "bash string timeout",
			tool: actuators.BashTool(),
			in:   map[string]any{"command": "true", "timeout": "60"},
			want: map[string]any{"command": "true", "timeout": int64(60)},
		},
		{
			name: "bash fractional integer string",
			tool: actuators.BashTool(),
			in:   map[string]any{"command": "true", "timeout": "60.0"},
			want: map[string]any{"command": "true", "timeout": int64(60)},
		},
		{
			name: "bash non-numeric timeout stays",
			tool: actuators.BashTool(),
			in:   map[string]any{"command": "true", "timeout": "soon"},
			want: map[string]any{"command": "true", "timeout": "soon"},
		},
		{
			name: "read integer and boolean strings",
			tool: actuators.ReadTool(),
			in:   map[string]any{"path": "a", "limit": "20", "offset": "5", "metadata": "true"},
			want: map[string]any{"path": "a", "limit": int64(20), "offset": int64(5), "metadata": true},
		},
		{
			name: "edit boolean string",
			tool: actuators.EditTool(),
			in:   map[string]any{"path": "a", "old_string": "x", "new_string": "y", "replace_all": "false"},
			want: map[string]any{"path": "a", "old_string": "x", "new_string": "y", "replace_all": false},
		},
		{
			name: "webfetch integer string",
			tool: actuators.WebFetchTool(),
			in:   map[string]any{"url": "https://example.com", "max_chars": "100"},
			want: map[string]any{"url": "https://example.com", "max_chars": int64(100)},
		},
		{
			name: "websearch integer string",
			tool: actuators.WebSearchTool(),
			in:   map[string]any{"query": "q", "num_results": "3"},
			want: map[string]any{"query": "q", "num_results": int64(3)},
		},
		{
			name: "delegate enum integer string",
			tool: delegate.DelegateSidecarLoopTool(),
			in:   map[string]any{"agent_name": "a", "task": "t", "acceptance_script": "true", "max_turns": "40"},
			want: map[string]any{"agent_name": "a", "task": "t", "acceptance_script": "true", "max_turns": int64(40)},
		},
		{
			name: "stringified array is parsed",
			tool: nested,
			in:   map[string]any{"items": `[{"count":"2"}]`},
			want: map[string]any{"items": []any{map[string]any{"count": int64(2)}}},
		},
		{
			name: "stringified object is parsed",
			tool: nested,
			in:   map[string]any{"meta": `{"enabled":"true"}`},
			want: map[string]any{"meta": map[string]any{"enabled": true}},
		},
		{
			name: "no parameter schema leaves the value alone",
			tool: contracts.Tool{Name: "plain"},
			in:   map[string]any{"anything": "1"},
			want: map[string]any{"anything": "1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeArguments(tc.tool, tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("arguments = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// The exact payload from the haiku session: the provider repairs the string
// before the agent loop's strict decoder sees it, so the call runs instead of
// failing with invalid_arguments.
func TestClaudeMessageRepairsAStringTimeout(t *testing.T) {
	result := claudeStreamResult{Output: &claudeStructuredOutput{
		ToolCalls: []claudeStructuredToolCall{{
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"true","timeout":"60"}`),
		}},
	}}
	message, err := claudeMessage(result, claudeTools{"bash": actuators.BashTool()})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	if got := message.ToolCalls[0].Function.Arguments; got != `{"command":"true","timeout":60}` {
		t.Fatalf("arguments = %s", got)
	}
	// The repaired call has to pass the same strict decoder the tool runs, or
	// the repair has only moved the failure one layer down.
	type bashArgs struct {
		Command string `json:"command"`
		Timeout *int   `json:"timeout"`
	}
	decoded, err := contracts.DecodeToolArguments[bashArgs](contracts.BashTool, message.ToolCalls[0])
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Timeout == nil || *decoded.Timeout != 60 {
		t.Fatalf("decoded timeout = %v", decoded.Timeout)
	}
}

// The native channel reaches the same converter, so a tool_use block that
// Claude Code rejected for being disabled still yields a runnable call.
func TestClaudeMessageRepairsANativeStringTimeout(t *testing.T) {
	result := claudeStreamResult{
		NativeTool: &claudeNativeToolCall{
			Name:  "bash",
			Input: json.RawMessage(`{"command":"true","timeout":"30"}`),
		},
	}
	message, err := claudeMessage(result, claudeTools{"bash": actuators.BashTool()})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	if got := message.ToolCalls[0].Function.Arguments; got != `{"command":"true","timeout":30}` {
		t.Fatalf("arguments = %s", got)
	}
}
