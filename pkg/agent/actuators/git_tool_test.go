package actuators

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestParseGitStatusV2(t *testing.T) {
	raw := strings.Join([]string{
		"# branch.oid 0123456789abcdef",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +2 -1",
		"1 M. N... 100644 100644 100644 abc abc staged.go",
		"1 .M N... 100644 100644 100644 abc abc unstaged.go",
		"2 R. N... 100644 100644 100644 abc abc R100 new.go",
		"old.go",
		"? untracked.txt",
		"u UU N... 100644 100644 100644 100644 a b c conflict.go",
		"",
	}, "\x00")

	branch, changes := parseGitStatusV2(raw)
	if branch.head != "main" || branch.upstream != "origin/main" || branch.ahead != 2 || branch.behind != 1 {
		t.Fatalf("branch = %+v", branch)
	}
	formatted := formatGitChanges(changes, 50)
	for _, want := range []string{
		"staged    M staged.go",
		"unstaged  M unstaged.go",
		"staged    R old.go -> new.go",
		"untracked untracked.txt",
		"conflict  UU conflict.go",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted status missing %q:\n%s", want, formatted)
		}
	}
}

func TestGitStatusReportsWorkspaceState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitTestRun(t, dir, "init")
	gitTestRun(t, dir, "config", "user.email", "test@example.com")
	gitTestRun(t, dir, "config", "user.name", "Test")
	gitTestRun(t, dir, "config", "commit.gpgsign", "false")
	branch := strings.TrimSpace(gitTestRun(t, dir, "symbolic-ref", "--short", "HEAD"))

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "initial")

	// One unstaged modification, one staged addition, one untracked file.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("loose\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := NewFileManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	output, err := manager.gitStatus(context.Background(), gitStatusArguments{MaxFiles: 50})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"branch " + branch,
		"last ",
		"A staged.txt",
		"M a.txt",
		"untracked.txt",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status missing %q:\n%s", want, output)
		}
	}

	withStat, err := manager.gitStatus(context.Background(), gitStatusArguments{MaxFiles: 50, Diffstat: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withStat, "unstaged:") || !strings.Contains(withStat, "a.txt") {
		t.Fatalf("diffstat missing:\n%s", withStat)
	}
}

func TestGitStatusOutsideRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	// Keep git from discovering a repository above the temp directory.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	manager, err := NewFileManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	output, err := manager.gitStatus(context.Background(), gitStatusArguments{MaxFiles: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "not a git repository") {
		t.Fatalf("output = %q, want a not-a-repository message", output)
	}
}

func TestGitStatusToolBudgetBoundsTheFileList(t *testing.T) {
	changes := make([]gitChange, 0, 5)
	for range 5 {
		changes = append(changes, gitChange{letters: ".M", path: "file.txt"})
	}
	formatted := formatGitChanges(changes, 2)
	if !strings.Contains(formatted, "(+3 more)") {
		t.Fatalf("formatted = %q, want a truncation marker", formatted)
	}
}

func gitTestRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// A workspace whose PATH has no git must fail with a clear environment error,
// not the opaque message a failed spawn would produce.
func TestGitStatusWithoutGitBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	manager, err := NewFileManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.gitStatus(context.Background(), gitStatusArguments{MaxFiles: 10})
	if err == nil {
		t.Fatal("git_status succeeded without a git binary")
	}
	var fileErr *filesystemError
	if !errors.As(err, &fileErr) || fileErr.code != "git_unavailable" {
		t.Fatalf("err = %v, want a git_unavailable error", err)
	}
	if fileErr.responsibility != contracts.ResponsibilityEnvironment {
		t.Fatalf("responsibility = %v, want environment", fileErr.responsibility)
	}
}
