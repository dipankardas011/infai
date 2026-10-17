package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestWriteReportNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	id := uuid.New()

	first, err := writeReport(dir, id, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	// Same id within the same second yields the same base name.
	second, err := writeReport(dir, id, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatalf("both reports were written to %s", first)
	}
	for path, want := range map[string]string{first: "first", second: "second"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !strings.HasPrefix(filepath.Base(first), "infai-report-"+id.String()) || filepath.Ext(first) != ".html" {
		t.Errorf("unexpected report name %s", filepath.Base(first))
	}
}

func TestSaveReportCmdWritesIntoTheSessionWorkspace(t *testing.T) {
	dir := t.TempDir()

	msg, ok := saveReportCmd(context.Background(), stubChatClient{}, uuid.New(), dir)().(reportSavedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if filepath.Dir(msg.path) != dir {
		t.Errorf("report saved to %s, want it under %s", msg.path, dir)
	}
	if data, err := os.ReadFile(msg.path); err != nil || string(data) != "<html></html>" {
		t.Errorf("saved content = %q, err = %v", data, err)
	}
}

func TestSaveReportCmdNeedsAWorkspace(t *testing.T) {
	msg := saveReportCmd(context.Background(), stubChatClient{}, uuid.New(), "")().(reportSavedMsg)
	if msg.err == nil {
		t.Fatal("an unknown workspace must be an error, not a write to the process cwd")
	}
}
