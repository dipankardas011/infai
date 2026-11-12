package actuators

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const (
	gitStatusDefaultMaxFiles = 200
	gitStatusMaxMaxFiles     = 1000
	gitStatusTimeout         = 5 * time.Second
	// gitRawOutputMax bounds one git command's stdout before parsing, so a huge
	// repository cannot make the tool allocate without bound.
	gitRawOutputMax = 1 << 20
)

type gitStatusArguments struct {
	Diffstat bool `json:"diffstat"`
	MaxFiles int  `json:"max_files"`
}

func GitStatusTool() contracts.Tool {
	return toolSchema(
		"git_status",
		"Read-only git snapshot of the workspace: current branch, upstream, ahead/behind, staged, unstaged, untracked and conflicted paths, the last commit, and an optional diffstat. Prefer this over running `git status` in the shell.",
		map[string]any{
			"diffstat": map[string]any{
				"type":        "boolean",
				"description": "Include staged and unstaged diffstat totals",
			},
			"max_files": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum changed paths to list per group; defaults to %d", gitStatusDefaultMaxFiles),
			},
		},
		nil,
	)
}

func (m *FileManager) GitStatusExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[gitStatusArguments](contracts.GitStatusTool, tc)
	if err != nil {
		return "", err
	}
	if err := gitStatusValidate(&args); err != nil {
		return "", wrapToolError(contracts.GitStatusTool, err, "invalid_arguments", "git_status arguments are invalid")
	}
	runCtx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
	defer cancel()
	output, err := m.gitStatus(runCtx, args)
	if err != nil {
		return "", wrapToolError(contracts.GitStatusTool, err, "git_status_failed", "the git status could not be read")
	}
	return output, nil
}

func gitStatusValidate(args *gitStatusArguments) error {
	if args.MaxFiles == 0 {
		args.MaxFiles = gitStatusDefaultMaxFiles
	}
	if args.MaxFiles < 1 || args.MaxFiles > gitStatusMaxMaxFiles {
		return contracts.NewToolExecutionError(contracts.GitStatusTool, "invalid_arguments", fmt.Sprintf("max_files must be between 1 and %d", gitStatusMaxMaxFiles), contracts.ResponsibilityAgent, nil)
	}
	return nil
}

func (m *FileManager) gitStatus(ctx context.Context, args gitStatusArguments) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", filesystemErr("git_unavailable", "git is not installed or not on PATH; install git to inspect repository status", contracts.ResponsibilityEnvironment, err)
	}
	top, err := m.runGit(ctx, m.root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if top.code != 0 {
		if strings.Contains(strings.ToLower(top.stderr), "not a git repository") {
			return "not a git repository", nil
		}
		return "", filesystemErr("git_unavailable", "git could not inspect the workspace", contracts.ResponsibilityEnvironment, errors.New(strings.TrimSpace(top.stderr)))
	}
	repo := strings.TrimSpace(top.stdout)

	status, err := m.runGit(ctx, repo, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	if status.code != 0 {
		return "", filesystemErr("git_status_failed", "git status failed", contracts.ResponsibilityEnvironment, errors.New(strings.TrimSpace(status.stderr)))
	}

	branch, changes := parseGitStatusV2(status.stdout)
	var out strings.Builder
	out.WriteString("repo " + repo + "\n")
	out.WriteString(gitBranchLine(branch) + "\n")
	if last := m.gitLastCommit(ctx, repo); last != "" {
		out.WriteString("last " + last + "\n")
	}
	out.WriteString(formatGitChanges(changes, args.MaxFiles))
	if args.Diffstat {
		if stat := m.gitDiffstat(ctx, repo); stat != "" {
			out.WriteString("\n" + stat)
		}
	}
	return clipWebContent(out.String(), maxToolContentBytes), nil
}

func (m *FileManager) gitLastCommit(ctx context.Context, repo string) string {
	run, err := m.runGit(ctx, repo, "log", "-1", "--format=%h%x09%ci%x09%s")
	if err != nil || run.code != 0 {
		return ""
	}
	fields := strings.SplitN(strings.TrimSpace(run.stdout), "\t", 3)
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
	}
	if len(fields) == 0 || fields[0] == "" {
		return ""
	}
	return strings.Join(fields, "  ·  ")
}

func (m *FileManager) gitDiffstat(ctx context.Context, repo string) string {
	var parts []string
	for _, spec := range []struct {
		label string
		args  []string
	}{
		{"unstaged", []string{"diff", "--stat"}},
		{"staged", []string{"diff", "--cached", "--stat"}},
	} {
		run, err := m.runGit(ctx, repo, spec.args...)
		if err != nil || run.code != 0 {
			continue
		}
		if text := strings.TrimSpace(run.stdout); text != "" {
			parts = append(parts, spec.label+":\n"+text)
		}
	}
	return strings.Join(parts, "\n")
}

type gitRun struct {
	stdout string
	stderr string
	code   int
}

// runGit runs git with a fixed argument list and no shell. Optional locks, the
// fsmonitor hook and path quoting are disabled so a read-only snapshot cannot
// run repository configuration or emit escaped paths.
func (m *FileManager) runGit(ctx context.Context, dir string, args ...string) (gitRun, error) {
	full := append([]string{
		"--no-optional-locks",
		"-c", "core.fsmonitor=false",
		"-c", "core.quotepath=false",
		"-C", dir,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = gitRawOutputMax, 8<<10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	run := gitRun{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			run.code = exitErr.ExitCode()
			return run, nil
		}
		return run, err
	}
	return run, nil
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	b   bytes.Buffer
	max int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.max <= 0 {
		return len(p), nil
	}
	if remaining := w.max - w.b.Len(); remaining > 0 {
		if len(p) > remaining {
			w.b.Write(p[:remaining])
		} else {
			w.b.Write(p)
		}
	}
	return len(p), nil
}

func (w *limitedBuffer) String() string { return w.b.String() }

type gitBranch struct {
	head     string
	oid      string
	upstream string
	ahead    int
	behind   int
}

type gitChange struct {
	// letters are the two porcelain XY letters, index then worktree; "." means
	// unchanged on that side.
	letters    string
	path       string
	orig       string
	untracked  bool
	conflicted bool
}

// parseGitStatusV2 reads `git status --porcelain=v2 --branch -z`. Records are
// NUL-terminated, and a rename/copy record is followed by one extra NUL-terminated
// token holding the original path.
func parseGitStatusV2(raw string) (gitBranch, []gitChange) {
	var branch gitBranch
	var changes []gitChange
	tokens := strings.Split(raw, "\x00")
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch {
		case strings.HasPrefix(token, "# branch.oid "):
			branch.oid = strings.TrimPrefix(token, "# branch.oid ")
		case strings.HasPrefix(token, "# branch.head "):
			branch.head = strings.TrimPrefix(token, "# branch.head ")
		case strings.HasPrefix(token, "# branch.upstream "):
			branch.upstream = strings.TrimPrefix(token, "# branch.upstream ")
		case strings.HasPrefix(token, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(token, "# branch.ab "))
			for _, field := range fields {
				if value, ok := strings.CutPrefix(field, "+"); ok {
					branch.ahead, _ = strconv.Atoi(value)
				}
				if value, ok := strings.CutPrefix(field, "-"); ok {
					branch.behind, _ = strconv.Atoi(value)
				}
			}
		case strings.HasPrefix(token, "1 "):
			fields := strings.SplitN(token, " ", 9)
			if len(fields) == 9 {
				changes = append(changes, gitChange{letters: fields[1], path: fields[8]})
			}
		case strings.HasPrefix(token, "2 "):
			fields := strings.SplitN(token, " ", 10)
			if len(fields) == 10 {
				change := gitChange{letters: fields[1], path: fields[9]}
				if index+1 < len(tokens) {
					change.orig = tokens[index+1]
					index++
				}
				changes = append(changes, change)
			}
		case strings.HasPrefix(token, "u "):
			fields := strings.SplitN(token, " ", 11)
			if len(fields) == 11 {
				changes = append(changes, gitChange{letters: fields[1], path: fields[10], conflicted: true})
			}
		case strings.HasPrefix(token, "? "):
			changes = append(changes, gitChange{letters: "??", path: strings.TrimPrefix(token, "? "), untracked: true})
		}
	}
	return branch, changes
}

func gitBranchLine(branch gitBranch) string {
	head := strings.TrimSpace(branch.head)
	if head == "" {
		head = "(unknown)"
	}
	line := "branch " + head
	if head == "(detached)" && branch.oid != "" && branch.oid != "(initial)" {
		short := branch.oid
		if len(short) > 7 {
			short = short[:7]
		}
		line = "branch (detached at " + short + ")"
	}
	if branch.upstream != "" {
		line += " (upstream " + branch.upstream
		if branch.ahead > 0 || branch.behind > 0 {
			line += fmt.Sprintf(", ahead %d, behind %d", branch.ahead, branch.behind)
		}
		line += ")"
	} else if head != "(detached)" && head != "(unknown)" && branch.oid != "(initial)" {
		line += " (no upstream)"
	}
	return line
}

func formatGitChanges(changes []gitChange, maxFiles int) string {
	var staged, unstaged, conflicts, untracked []string
	for _, change := range changes {
		switch {
		case change.conflicted:
			conflicts = append(conflicts, gitChangeLine(change.letters, change))
		case change.untracked:
			untracked = append(untracked, change.path)
		default:
			if len(change.letters) == 2 && change.letters[0] != '.' {
				staged = append(staged, gitChangeLine(string(change.letters[0]), change))
			}
			if len(change.letters) == 2 && change.letters[1] != '.' {
				unstaged = append(unstaged, gitChangeLine(string(change.letters[1]), change))
			}
		}
	}
	if len(staged)+len(unstaged)+len(conflicts)+len(untracked) == 0 {
		return "working tree clean"
	}
	var out strings.Builder
	writeGitGroup(&out, "staged", staged, maxFiles)
	writeGitGroup(&out, "unstaged", unstaged, maxFiles)
	writeGitGroup(&out, "conflict", conflicts, maxFiles)
	writeGitGroup(&out, "untracked", untracked, maxFiles)
	return strings.TrimRight(out.String(), "\n")
}

func gitChangeLine(mark string, change gitChange) string {
	if change.orig != "" {
		return mark + " " + change.orig + " -> " + change.path
	}
	return mark + " " + change.path
}

func writeGitGroup(out *strings.Builder, label string, items []string, maxFiles int) {
	if len(items) == 0 {
		return
	}
	limit := min(len(items), maxFiles)
	for _, item := range items[:limit] {
		out.WriteString(fmt.Sprintf("%-9s %s\n", label, item))
	}
	if len(items) > limit {
		out.WriteString(fmt.Sprintf("%-9s ... (+%d more)\n", label, len(items)-limit))
	}
}
