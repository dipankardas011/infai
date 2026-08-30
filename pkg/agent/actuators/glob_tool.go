package actuators

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

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

func (m *FileManager) Glob(pattern string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pattern == "" {
		return nil, filesystemErr("invalid_pattern", "the glob pattern must not be empty", ResponsibilityAgent, nil)
	}
	if filepath.IsAbs(pattern) || strings.ContainsRune(pattern, 0) {
		return nil, filesystemErr("invalid_path", "the glob pattern must be workspace-relative", ResponsibilityAgent, nil)
	}
	if err := validateText(pattern, ResponsibilityAgent); err != nil {
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
			if err := validateText(relative, ResponsibilityTool); err != nil {
				return err
			}
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return filesystemErr("path_unavailable", "a matching path could not be resolved", ResponsibilityEnvironment, resolveErr)
			}
			if !withinDirectory(m.root, resolved) {
				return filesystemErr("path_outside_workspace", "a matching path is outside the workspace", ResponsibilityAgent, nil)
			}
			out = append(out, relative)
			if len(out) > maxDirectoryEntries {
				return filesystemErr("too_many_matches", "the glob matched too many paths", ResponsibilityTool, nil)
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

func normalizeGlobPattern(pattern string) (string, error) {
	segments := strings.Split(strings.Trim(filepath.ToSlash(pattern), "/"), "/")
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return "", filesystemErr("path_outside_workspace", "the pattern must remain inside the workspace", ResponsibilityAgent, nil)
		}
		if segment == "**" {
			clean = append(clean, segment)
			continue
		}
		if _, err := filepath.Match(segment, ""); err != nil {
			return "", filesystemErr("invalid_pattern", "the glob pattern is malformed", ResponsibilityAgent, err)
		}
		clean = append(clean, segment)
	}
	if len(clean) == 0 {
		return "", filesystemErr("invalid_pattern", "the glob pattern must contain a path", ResponsibilityAgent, nil)
	}
	return strings.Join(clean, "/"), nil
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

func globExecution(ctx context.Context) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	_, err := decodeArgs(ctx, &args)
	if err != nil || args.Pattern == "" {
		return "", filesystemErr("invalid_arguments", "glob requires pattern", ResponsibilityAgent, err)
	}
	m, err := fileManager(ctx)
	if err != nil {
		return "", err
	}
	matches, err := m.Glob(args.Pattern)
	output, marshalErr := mustJSON(matches)
	if err == nil {
		err = marshalErr
	}
	return string(output), err
}
