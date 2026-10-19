package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestScanWorkspaceFilesIncludesHiddenAndExcludesGitAndNodeModules(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", ".hidden/config", ".git/config", "node_modules/pkg/index.js"} {
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

func TestWalkWorkspaceFilesStreamsBatches(t *testing.T) {
	root := t.TempDir()
	total := workspaceBatchSize + 3
	for i := 0; i < total; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%05d.go", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var batches []int
	walkWorkspaceFiles(root, func(batch []string) bool {
		batches = append(batches, len(batch))
		return true
	})

	if len(batches) != 2 || batches[0] != workspaceBatchSize || batches[1] != 3 {
		t.Fatalf("batches=%v", batches)
	}
}

func TestWalkWorkspaceFilesStopsWhenTheConsumerDoes(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < workspaceBatchSize*2; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%05d.go", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var batches []int
	walkWorkspaceFiles(root, func(batch []string) bool {
		batches = append(batches, len(batch))
		return false
	})

	if len(batches) != 1 {
		t.Fatalf("walk kept going after the consumer stopped: %v", batches)
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
func TestSlashMenuIncludesBackendSkills(t *testing.T) {
	skills := []contracts.Skill{{Title: "code-review", Description: "Review code"}, {Title: "ml-researcher", Description: "Research ML"}}
	matches := matchingCommands("/", nil, skills)

	got := make(map[string]string)
	for _, match := range matches {
		if match.insertAs != "" {
			got[match.name] = match.insertion()
		}
	}
	want := map[string]string{
		"/skill:code-review":   "/skill:code-review",
		"/skill:ml-researcher": "/skill:ml-researcher",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skill commands=%v want %v", got, want)
	}
	if got := matches[0].menuText(); got != "/skill:code-review · Review code" {
		t.Fatalf("skill menu text=%q", got)
	}
}

func TestSlashMenuSelectsBackendSkill(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.availableSkills = []contracts.Skill{{Title: "code-review"}, {Title: "ml-researcher"}}
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	if !m.commandMenu {
		t.Fatal("slash did not open the menu")
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got, want := m.composer.Value(), "/skill:code-review"; got != want {
		t.Fatalf("composer=%q want %q", got, want)
	}
}

func TestEncodeAndRenderSkillReferences(t *testing.T) {
	got := encodeSkillReferences("use /skill:code-review and /skill:ml_researcher")
	if want := "use [skill:code-review] and [skill:ml_researcher]"; got != want {
		t.Fatalf("encoded %q want %q", got, want)
	}
	got = renderSkillReferences(got)
	if want := "use /skill:code-review and /skill:ml_researcher"; got != want {
		t.Fatalf("rendered %q want %q", got, want)
	}
}

func TestEncodeAndRenderFileReferences(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "pkg/b.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := encodeFileReferences("compare @a.go and @pkg/b.go", root)
	if want := "compare [file:a.go] and [file:pkg/b.go]"; got != want {
		t.Fatalf("encoded %q want %q", got, want)
	}
	got = renderFileReferences(got)
	if want := "compare @a.go and @pkg/b.go"; got != want {
		t.Fatalf("rendered %q want %q", got, want)
	}

	got = encodeFileReferences("read @a.go, then @pkg/b.go.", root)
	if want := "read [file:a.go], then [file:pkg/b.go]."; got != want {
		t.Fatalf("encoded punctuation %q want %q", got, want)
	}
}
