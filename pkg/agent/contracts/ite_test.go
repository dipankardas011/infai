package contracts

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
