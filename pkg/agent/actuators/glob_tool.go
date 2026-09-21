package actuators

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type globArguments struct {
	Pattern string `json:"pattern"`
}

func GlobTool() contracts.Tool {
	return toolSchema(
		"glob",
		"Find files and directories matching a workspace-relative pattern",
		map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Path pattern such as *.go or **/*.go",
			},
		},
		[]string{"pattern"},
	)
}

func (m *FileManager) GlobExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[globArguments](contracts.GlobTool, tc)
	if err != nil {
		return "", err
	}
	if err := globToolValidate(args); err != nil {
		return "", wrapToolError(contracts.GlobTool, err, "invalid_arguments", "glob arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.GlobTool, 10*time.Second, func() (string, error) {
		matches, err := m.glob(args.Pattern)
		if err != nil {
			return "", wrapToolError(contracts.GlobTool, err, "glob_failed", "the pattern could not be matched")
		}
		return assemble(matches)
	})
}

func (m *FileManager) glob(pattern string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pattern == "" {
		return nil, filesystemErr("invalid_pattern", "the glob pattern must not be empty", contracts.ResponsibilityAgent, nil)
	}
	if filepath.IsAbs(pattern) || strings.ContainsRune(pattern, 0) {
		return nil, filesystemErr("invalid_path", "the glob pattern must be workspace-relative", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(pattern, contracts.ResponsibilityAgent); err != nil {
		return nil, err
	}
	pattern, err := normalizeGlobPattern(pattern)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	err = filepath.WalkDir(m.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(m.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			relative = ""
		}
		if globMatch(pattern, relative) {
			if err := validateText(relative, contracts.ResponsibilityTool); err != nil {
				return err
			}
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return filesystemErr("path_unavailable", "a matching path could not be resolved", contracts.ResponsibilityEnvironment, resolveErr)
			}
			if !withinDirectory(m.root, resolved) {
				return filesystemErr("path_outside_workspace", "a matching path is outside the workspace", contracts.ResponsibilityAgent, nil)
			}
			out = append(out, relative)
			if len(out) > maxDirectoryEntries {
				return filesystemErr("too_many_matches", "the glob matched too many paths; narrow the pattern or directory", contracts.ResponsibilityTool, nil)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func globMatch(pattern, path string) bool {
	patterns := strings.Split(strings.Trim(pattern, "/"), "/")
	paths := strings.Split(strings.Trim(path, "/"), "/")
	if len(paths) == 1 && paths[0] == "" {
		paths = nil
	}
	var match func(int, int) bool
	match = func(pi, si int) bool {
		if pi == len(patterns) {
			return si == len(paths)
		}
		if patterns[pi] == "**" {
			return match(pi+1, si) || si < len(paths) && match(pi, si+1)
		}
		if si == len(paths) {
			return false
		}
		ok, err := filepath.Match(patterns[pi], paths[si])
		return err == nil && ok && match(pi+1, si+1)
	}
	return match(0, 0)
}

func normalizeGlobPattern(pattern string) (string, error) {
	segments := strings.Split(strings.Trim(filepath.ToSlash(pattern), "/"), "/")
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return "", filesystemErr("path_outside_workspace", "the pattern must remain inside the workspace", contracts.ResponsibilityAgent, nil)
		}
		if segment == "**" {
			clean = append(clean, segment)
			continue
		}
		if _, err := filepath.Match(segment, ""); err != nil {
			return "", filesystemErr("invalid_pattern", "the glob pattern is malformed", contracts.ResponsibilityAgent, err)
		}
		clean = append(clean, segment)
	}
	if len(clean) == 0 {
		return "", filesystemErr("invalid_pattern", "the glob pattern must contain a path", contracts.ResponsibilityAgent, nil)
	}
	return strings.Join(clean, "/"), nil
}

func globToolValidate(args globArguments) error {
	if args.Pattern == "" {
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", "glob requires pattern", contracts.ResponsibilityAgent, nil)
	}
	return nil
}
