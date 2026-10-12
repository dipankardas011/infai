package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestObjectSchemaIsCanonical(t *testing.T) {
	schema := ToolParameterObjectSchema(map[string]any{"path": map[string]any{"type": "string"}}, []string{"path"})
	want := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`
	if string(schema) != want {
		t.Fatalf("schema = %s, want %s", schema, want)
	}
	// A nil required list is omitted rather than serialised as a null value: a
	// strict provider rejects a null where an array is expected, and the absent
	// key is the valid spelling of "nothing is required".
	// § deepseek-flash schema validation
	const noRequired = `{"type":"object","properties":{},"additionalProperties":false}`
	if empty := ToolParameterObjectSchema(map[string]any{}, nil); string(empty) != noRequired {
		t.Fatalf("empty schema = %s, want %s", empty, noRequired)
	}
	if nilProperties := ToolParameterObjectSchema(nil, nil); string(nilProperties) != noRequired {
		t.Fatalf("nil-properties schema = %s, want %s", nilProperties, noRequired)
	}
}

// A tool's parameters are forwarded verbatim, so a schema using keywords outside
// the built-in subset must survive unchanged (§ MCP inputSchema).
func TestToolParametersAreForwardedVerbatim(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","$defs":{"path":{"type":"string"}},"properties":{"path":{"$ref":"#/$defs/path"}},"additionalProperties":{"type":"string"},"x-extension":true}`)
	tool := Tool{Name: "t", Description: "d", Parameters: schema}
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Parameters) != string(schema) {
		t.Fatalf("parameters = %s, want %s", decoded.Parameters, schema)
	}
}

func TestDecodeToolArgumentsRejectsMalformedInput(t *testing.T) {
	type readArgs struct {
		Path string `json:"path"`
	}
	call := func(arguments string) ToolCall {
		return ToolCall{Function: Function{Name: ReadTool, Arguments: arguments}}
	}
	for _, arguments := range []string{"", "null", "[]", `"text"`, `{"path":"a"} {}`, `{"path":"a"} {"x":1}`} {
		if _, err := DecodeToolArguments[readArgs](ReadTool, call(arguments)); err == nil {
			t.Fatalf("arguments %q decoded without error", arguments)
		}
	}

	if _, err := DecodeToolArguments[readArgs](ReadTool, call(`{"path":"a","junk":1}`)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	decoded, err := DecodeToolArguments[readArgs](ReadTool, call(`{"path":"a"}`))
	if err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	if decoded.Path != "a" {
		t.Fatalf("decoded path = %q", decoded.Path)
	}
}

func TestRunBoundedReportsOnlyItsOwnDeadline(t *testing.T) {
	blocked := func() (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := RunBounded(ctx, ReadTool, time.Second, blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("outer deadline error = %v, want context.DeadlineExceeded", err)
	}
	if executionErr, ok := errors.AsType[*ExecutionError](err); ok {
		t.Fatalf("outer deadline reported as tool failure %q", executionErr.Code)
	}

	_, err = RunBounded(context.Background(), ReadTool, time.Millisecond, blocked)
	executionErr, ok := errors.AsType[*ExecutionError](err)
	if !ok || executionErr.Code != "execution_timeout" {
		t.Fatalf("tool timeout error = %v, want execution_timeout", err)
	}
}
