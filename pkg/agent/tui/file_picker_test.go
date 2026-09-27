package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestScanWorkspaceFilesIncludesHiddenAndExcludesGit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", ".hidden/config", ".git/config"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{".hidden/config", "main.go"}
	if got := scanWorkspaceFiles(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("files=%v want %v", got, want)
	}
}

func TestFuzzyFileMatches(t *testing.T) {
	files := []string{"pkg/agent/tui/chat_test.go", "pkg/agent/tui/chat.go", "README.md"}
	got := fuzzyFileMatches(files, "chatg", 12)
	want := []string{"pkg/agent/tui/chat.go", "pkg/agent/tui/chat_test.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches=%v want %v", got, want)
	}
}

func TestFilePickerSelectionReplacesQuery(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.composer.SetValue("explain @pkgds/set")
	m.filePicker = &filePicker{
		matches: []string{"pkg/ds/set.go"},
		query:   "pkgds/set",
		start:   len("explain "),
	}

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got, want := m.composer.Value(), "explain @pkg/ds/set.go"; got != want {
		t.Fatalf("composer=%q want %q", got, want)
	}
}
func TestEncodeAndRenderFileReferences(t *testing.T) {
	got := encodeFileReferences("compare @a.go and @pkg/b.go", []string{"a.go", "pkg/b.go"})
	if want := "compare [file:a.go] and [file:pkg/b.go]"; got != want {
		t.Fatalf("encoded %q want %q", got, want)
	}
	got = renderFileReferences(got)
	if want := "compare @a.go and @pkg/b.go"; got != want {
		t.Fatalf("rendered %q want %q", got, want)
	}
}
