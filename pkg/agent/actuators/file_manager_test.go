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

func TestFileManagerSnapshotAndEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Read("note.txt", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Edit("note.txt", "two", "three", false); err == nil {
		t.Fatal("expected duplicate edit to require replace_all")
	}
	if _, err = m.Edit("note.txt", "two", "three", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one\nthree\nthree\n" {
		t.Fatalf("content = %q", data)
	}
	if err = os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = m.Write("note.txt", "overwrite\n"); err == nil || !strings.Contains(err.Error(), "must be read") {
		t.Fatalf("write error = %v, want snapshot conflict", err)
	}
}

func TestFileManagerMetadataDoesNotAuthorizeMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := mustManager(t, root)
	if _, err := m.Read("note.txt", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Write("note.txt", "changed\n"); err == nil || !strings.Contains(err.Error(), "must be read") {
		t.Fatalf("write error = %v, want read-before-write error", err)
	}
}

func TestFileManagerRejectsOutsideAndAllowsNewFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Read("../secret", nil, nil, false); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if err = m.Write("new.txt", "created"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(got) != "created" {
		t.Fatalf("new file = %q", got)
	}
}

func TestFileManagerGlobSupportsRecursivePatterns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "root.go"),
		filepath.Join(root, "nested", "nested.go"),
		filepath.Join(root, "nested", "deep", "deep.go"),
	} {
		if err := os.WriteFile(path, []byte("package test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := mustManager(t, root)
	matches, err := m.Glob("**/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %v, want 3 Go files", matches)
	}
}

func TestFileManagerRejectsSymlinkedParentOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m := mustManager(t, root)
	if err := m.Write("external/new.txt", "secret"); err == nil {
		t.Fatal("expected symlinked parent rejection")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file stat error = %v", err)
	}
}

func TestFileManagerRejectsEmptyEditString(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := mustManager(t, root)
	if _, err := m.Read("note.txt", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Edit("note.txt", "", "replacement", false); err == nil {
		t.Fatal("expected empty old_string rejection")
	}
}

func TestFileToolDispatcherBasics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithFileManager(context.Background(), mustManager(t, root))
	for _, call := range []contracts.ToolCall{
		{Function: contracts.Function{Name: string(contracts.ListTool), Arguments: `{}`}},
		{Function: contracts.Function{Name: string(contracts.GlobTool), Arguments: `{"pattern":"*.txt"}`}},
		{Function: contracts.Function{Name: string(contracts.SearchTool), Arguments: `{"pattern":"needle"}`}},
	} {
		out, err := ExecuteToolCall(ctx, call)
		if err != nil {
			t.Fatalf("%s: %v", call.Function.Name, err)
		}
		var value any
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatalf("%s output = %q: %v", call.Function.Name, out, err)
		}
	}
}

func mustManager(t *testing.T, root string) *FileManager {
	t.Helper()
	m, err := NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
