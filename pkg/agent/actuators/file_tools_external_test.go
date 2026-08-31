package actuators_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestFileToolsHaveDescriptiveSchemas(t *testing.T) {
	tools := []contracts.Tool{
		actuators.ReadTool(),
		actuators.WriteTool(),
		actuators.EditTool(),
		actuators.ListTool(),
		actuators.GlobTool(),
		actuators.SearchTool(),
		actuators.BashTool(),
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Description == "" {
			t.Fatalf("tool schema = %+v, want name and description", tool)
		}
		if tool.Parameters.Type != "object" || tool.Parameters.AdditionalProperties {
			t.Fatalf("%s parameters = %+v", tool.Name, tool.Parameters)
		}
	}
}

func TestReadToolSupportsRangesAndMetadataExclusivity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "note.txt", "one\ntwo\nthree\n")
	m := manager(t, root)

	output := execute(t, m, contracts.ReadTool, map[string]any{
		"path": "note.txt", "offset": 2, "limit": 1,
	})
	if output != "two\n" {
		t.Fatalf("read output = %q", output)
	}

	err := executeError(t, m, contracts.ReadTool, map[string]any{
		"path": "note.txt", "metadata": true, "limit": 1,
	})
	if !strings.Contains(err, "invalid_arguments") {
		t.Fatalf("metadata error = %s", err)
	}
}

func TestWriteToolRequiresReadForExistingFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "note.txt", "before")
	m := manager(t, root)

	err := executeError(t, m, contracts.WriteTool, map[string]any{
		"path": "note.txt", "content": "after",
	})
	if !strings.Contains(err, "file_changed_since_read") {
		t.Fatalf("write error = %s", err)
	}

	execute(t, m, contracts.ReadTool, map[string]any{"path": "note.txt"})
	execute(t, m, contracts.WriteTool, map[string]any{
		"path": "note.txt", "content": "after",
	})
	if got := readFile(t, filepath.Join(root, "note.txt")); got != "after" {
		t.Fatalf("written content = %q", got)
	}
}

func TestEditToolRequiresUniqueExactMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "note.txt", "same\nsame\n")
	m := manager(t, root)
	execute(t, m, contracts.ReadTool, map[string]any{"path": "note.txt"})

	err := executeError(t, m, contracts.EditTool, map[string]any{
		"path": "note.txt", "old_string": "same", "new_string": "changed",
	})
	if !strings.Contains(err, "use replace_all") {
		t.Fatalf("edit error = %s", err)
	}
	execute(t, m, contracts.EditTool, map[string]any{
		"path": "note.txt", "old_string": "same", "new_string": "changed", "replace_all": true,
	})
}

func TestFileToolsRejectInvisibleCharacters(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "note.txt", "safe")
	m := manager(t, root)

	err := executeError(t, m, contracts.ReadTool, map[string]any{"path": "bad\u200b.txt"})
	if !strings.Contains(err, "invisible_character") {
		t.Fatalf("path error = %s", err)
	}
	for _, character := range []string{"\u202e", "\uFE0F", "\U000E0000", "\uFDD0"} {
		err = executeError(t, m, contracts.WriteTool, map[string]any{
			"path": "new.txt", "content": "bad" + character + "content",
		})
		if !strings.Contains(err, "invisible_character") {
			t.Fatalf("content %U error = %s", []rune(character)[0], err)
		}
	}
	execute(t, m, contracts.WriteTool, map[string]any{"path": "allowed.txt", "content": "line 1\nline 2\tvalue"})
}

func TestGlobListAndSearchToolBehavior(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "root.go", "needle\n")
	writeFile(t, root, filepath.Join("nested", "deep", "deep.go"), "needle\n")
	writeFile(t, root, ".hidden", "needle\n")
	m := manager(t, root)

	var matches []string
	if err := json.Unmarshal([]byte(execute(t, m, contracts.GlobTool, map[string]any{
		"pattern": "**/*.go",
	})), &matches); err != nil || len(matches) != 2 {
		t.Fatalf("glob matches = %v, error = %v", matches, err)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(execute(t, m, contracts.ListTool, map[string]any{})), &entries); err != nil {
		t.Fatalf("list output: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("list returned no entries")
	}

	var results []map[string]any
	if err := json.Unmarshal([]byte(execute(t, m, contracts.SearchTool, map[string]any{
		"pattern": "needle", "path": ".",
	})), &results); err != nil || len(results) != 2 {
		t.Fatalf("search results = %v, error = %v", results, err)
	}
}

func TestGlobToolRejectsMalformedPatterns(t *testing.T) {
	m := manager(t, t.TempDir())
	err := executeError(t, m, contracts.GlobTool, map[string]any{"pattern": "["})
	if !strings.Contains(err, "invalid_pattern") {
		t.Fatalf("glob error = %s", err)
	}
}

func TestFileToolsRejectMalformedJSONAndOverflow(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "note.txt", "content")
	m := manager(t, root)

	if err := executeRawError(t, m, contracts.ListTool, "null"); !strings.Contains(err, "invalid_arguments") {
		t.Fatalf("null arguments error = %s", err)
	}
	if err := executeRawError(t, m, contracts.ReadTool, `{"path":"note.txt"} {}`); !strings.Contains(err, "invalid_arguments") {
		t.Fatalf("trailing arguments error = %s", err)
	}
	if output := executeRaw(t, m, contracts.ReadTool, `{"path":"note.txt","offset":2,"limit":9223372036854775807}`); output != "" {
		t.Fatalf("overflow-range output = %q, want empty output", output)
	}
}

func TestFileToolsRejectInvisibleOutputCharacters(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "bad\u200b.txt", "needle")
	m := manager(t, root)
	if err := executeError(t, m, contracts.ListTool, map[string]any{}); !strings.Contains(err, "invisible_character") {
		t.Fatalf("list error = %s", err)
	}

	writeFile(t, root, "content.txt", "needle\u202ehidden")
	if err := executeError(t, m, contracts.SearchTool, map[string]any{"pattern": "needle"}); !strings.Contains(err, "invisible_character") {
		t.Fatalf("search error = %s", err)
	}
}

func TestFileToolsReturnEmptyArraysForNoMatches(t *testing.T) {
	m := manager(t, t.TempDir())
	if output := execute(t, m, contracts.GlobTool, map[string]any{"pattern": "*.missing"}); output != "[]" {
		t.Fatalf("glob output = %q, want []", output)
	}
	if output := execute(t, m, contracts.SearchTool, map[string]any{"pattern": "missing"}); output != "[]" {
		t.Fatalf("search output = %q, want []", output)
	}
}

func manager(t *testing.T, root string) *actuators.FileManager {
	t.Helper()
	m, err := actuators.NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func execute(t *testing.T, manager *actuators.FileManager, tool contracts.ToolType, args map[string]any) string {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	output, err := actuators.ExecuteToolCall(
		actuators.WithFileManager(context.Background(), manager),
		contracts.ToolCall{Function: contracts.Function{Name: string(tool), Arguments: string(data)}},
	)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return output
}

func executeError(t *testing.T, manager *actuators.FileManager, tool contracts.ToolType, args map[string]any) string {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return executeRawError(t, manager, tool, string(data))
}

func executeRawError(t *testing.T, manager *actuators.FileManager, tool contracts.ToolType, arguments string) string {
	t.Helper()
	_, err := actuators.ExecuteToolCall(
		actuators.WithFileManager(context.Background(), manager),
		contracts.ToolCall{Function: contracts.Function{Name: string(tool), Arguments: arguments}},
	)
	if err == nil {
		t.Fatalf("%s: expected error", tool)
	}
	return err.Error()
}

func executeRaw(t *testing.T, manager *actuators.FileManager, tool contracts.ToolType, arguments string) string {
	t.Helper()
	output, err := actuators.ExecuteToolCall(
		actuators.WithFileManager(context.Background(), manager),
		contracts.ToolCall{Function: contracts.Function{Name: string(tool), Arguments: arguments}},
	)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return output
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
