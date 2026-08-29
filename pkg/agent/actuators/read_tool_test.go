package actuators

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func readCall(path string) contracts.ToolCall {
	return contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.ReadTool),
			Arguments: `{"path":"` + path + `"}`,
		},
	}
}

func TestReadExecutionReadsWorkspaceRelativeUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello\nनमस्ते"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, readCall("note.txt"))
	output, err := readExecution(ctx)
	if err != nil {
		t.Fatalf("readExecution: %v", err)
	}
	if output != "hello\nनमस्ते" {
		t.Fatalf("output = %q", output)
	}
}

func TestReadExecutionRejectsPathsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, readCall("../secret.txt"))

	_, err := readExecution(ctx)
	if err == nil || !strings.Contains(err.Error(), "path_outside_workspace") {
		t.Fatalf("error = %v, want path_outside_workspace", err)
	}
}

func TestReadExecutionRequiresWorkspace(t *testing.T) {
	ctx := WithToolCall(context.Background(), readCall("note.txt"))
	_, err := readExecution(ctx)
	if err == nil || !strings.Contains(err.Error(), "missing_workspace") {
		t.Fatalf("error = %v, want missing_workspace", err)
	}
}

func TestReadExecutionRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, readCall("binary"))

	_, err := readExecution(ctx)
	if err == nil || !strings.Contains(err.Error(), "invalid_utf8") {
		t.Fatalf("error = %v, want invalid_utf8", err)
	}
}

func TestReadExecutionReadsLineRange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.ReadTool),
			Arguments: `{"path":"note.txt","offset":2,"limit":1}`,
		},
	})
	output, err := readExecution(ctx)
	if err != nil {
		t.Fatalf("readExecution: %v", err)
	}
	if output != "two\n" {
		t.Fatalf("output = %q, want %q", output, "two\n")
	}
}

func TestReadExecutionMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.ReadTool),
			Arguments: `{"path":"note.txt","metadata":true}`,
		},
	})
	output, err := readExecution(ctx)
	if err != nil {
		t.Fatalf("readExecution: %v", err)
	}
	var metadata readMetadata
	if err := json.Unmarshal([]byte(output), &metadata); err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	if !metadata.UTF8 || metadata.LineCount == nil || *metadata.LineCount != 2 {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.Permissions != "0600" || metadata.SizeBytes == 0 || metadata.DiskSizeBytes == 0 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestReadExecutionRejectsMetadataWithRange(t *testing.T) {
	root := t.TempDir()
	ctx := WithWorkspace(context.Background(), root)
	ctx = WithToolCall(ctx, contracts.ToolCall{
		Function: contracts.Function{
			Name:      string(contracts.ReadTool),
			Arguments: `{"path":"note.txt","metadata":true,"limit":1}`,
		},
	})

	_, err := readExecution(ctx)
	if err == nil || !strings.Contains(err.Error(), "metadata cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}
