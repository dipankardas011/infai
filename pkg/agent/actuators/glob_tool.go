package actuators

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const (
	globTypeAny  = "any"
	globTypeFile = "file"
	globTypeDir  = "dir"

	globDefaultLimit = 200
	globMaxLimit     = 1000
)

func GlobTool() contracts.Tool {
	return toolSchema(
		"glob",
		"Find files or directories matching a workspace-relative pattern (*, **, ?, [...]). Skips hidden paths and .git unless include_hidden is true. Returns one path per line; a trailing [truncated: ...] line means more matches exist.",
		map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Path pattern such as *.go or **/*.go",
			},
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{globTypeAny, globTypeFile, globTypeDir},
				"description": "Return files, directories, or both; defaults to any",
			},
			"exclude": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Skip paths matching one of these globs; a glob without a slash matches a basename at any depth",
			},
			"max_depth": map[string]any{
				"type":        "integer",
				"description": "Maximum path depth below the workspace root; 0 means unlimited",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "Match without regard to case",
			},
			"include_hidden": map[string]any{
				"type":        "boolean",
				"description": "Include dotfiles and dot-directories; .git is always skipped",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum paths to return; defaults to %d", globDefaultLimit),
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Number of matches to skip; use with limit to page",
			},
		},
		[]string{"pattern"},
	)
}

type globArguments struct {
	Pattern         string   `json:"pattern"`
	Type            string   `json:"type"`
	Exclude         []string `json:"exclude"`
	MaxDepth        int      `json:"max_depth"`
	CaseInsensitive bool     `json:"case_insensitive"`
	IncludeHidden   bool     `json:"include_hidden"`
	Limit           int      `json:"limit"`
	Offset          int      `json:"offset"`
}

func (m *FileManager) GlobExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[globArguments](contracts.GlobTool, tc)
	if err != nil {
		return "", err
	}
	if err := globToolValidate(&args); err != nil {
		return "", wrapToolError(contracts.GlobTool, err, "invalid_arguments", "glob arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.GlobTool, 10*time.Second, func() (string, error) {
		matches, truncated, err := m.glob(args)
		if err != nil {
			return "", wrapToolError(contracts.GlobTool, err, "glob_failed", "the pattern could not be matched")
		}
		return formatGlobMatches(matches, truncated), nil
	})
}

func globToolValidate(args *globArguments) error {
	if args.Pattern == "" {
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", "glob requires pattern", contracts.ResponsibilityAgent, nil)
	}
	if _, err := normalizeGlobPattern(args.Pattern); err != nil {
		return err
	}
	switch args.Type {
	case "", globTypeAny:
		args.Type = globTypeAny
	case globTypeFile, globTypeDir:
	default:
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", "type must be any, file, or dir", contracts.ResponsibilityAgent, nil)
	}
	if args.Limit == 0 {
		args.Limit = globDefaultLimit
	}
	if args.Limit < 1 || args.Limit > globMaxLimit {
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", fmt.Sprintf("limit must be between 1 and %d", globMaxLimit), contracts.ResponsibilityAgent, nil)
	}
	if args.Offset < 0 {
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", "offset must not be negative", contracts.ResponsibilityAgent, nil)
	}
	if args.MaxDepth < 0 {
		return contracts.NewToolExecutionError(contracts.GlobTool, "invalid_arguments", "max_depth must not be negative", contracts.ResponsibilityAgent, nil)
	}
	for index, pattern := range args.Exclude {
		normalized, err := normalizeGlobPattern(pattern)
		if err != nil {
			return err
		}
		args.Exclude[index] = normalized
	}
	return nil
}

func (m *FileManager) glob(args globArguments) ([]string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pattern, err := normalizeGlobPattern(args.Pattern)
	if err != nil {
		return nil, false, err
	}
	if args.Type == "" {
		args.Type = globTypeAny
	}
	if args.Limit <= 0 {
		args.Limit = globDefaultLimit
	}
	matchPattern := pattern
	if args.CaseInsensitive {
		matchPattern = strings.ToLower(matchPattern)
	}

	matches := make([]string, 0)
	seen := 0
	written := 0
	bytes := 0
	truncated := false

	err = filepath.WalkDir(m.root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(m.root, filePath)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relative)
		if rel == "." {
			rel = ""
		}
		if rel == "" {
			return nil
		}
		depth := strings.Count(rel, "/") + 1

		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if !args.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if globMatchesAny(args.Exclude, rel) {
				return filepath.SkipDir
			}
			if args.MaxDepth > 0 && depth > args.MaxDepth {
				return filepath.SkipDir
			}

			if args.Type == globTypeAny || args.Type == globTypeDir {
				subject := rel
				if args.CaseInsensitive {
					subject = strings.ToLower(subject)
				}
				if globMatch(matchPattern, subject) {
					seen++
					if seen > args.Offset {
						if written >= args.Limit || bytes+len(rel)+1 > maxToolContentBytes {
							truncated = true
							return filepath.SkipAll
						}
						if err := m.checkMatchPath(filePath); err != nil {
							return err
						}
						matches = append(matches, rel)
						written++
						bytes += len(rel) + 1
					}
				}
			}
			if args.MaxDepth > 0 && depth >= args.MaxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if !args.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if globMatchesAny(args.Exclude, rel) {
			return nil
		}
		if args.MaxDepth > 0 && depth > args.MaxDepth {
			return nil
		}
		if args.Type == globTypeDir {
			return nil
		}

		subject := rel
		if args.CaseInsensitive {
			subject = strings.ToLower(subject)
		}
		if !globMatch(matchPattern, subject) {
			return nil
		}

		seen++
		if seen <= args.Offset {
			return nil
		}
		if written >= args.Limit || bytes+len(rel)+1 > maxToolContentBytes {
			truncated = true
			return filepath.SkipAll
		}
		if err := m.checkMatchPath(filePath); err != nil {
			return err
		}
		matches = append(matches, rel)
		written++
		bytes += len(rel) + 1
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return matches, truncated, nil
}

// checkMatchPath rejects a match whose symlinks lead outside the workspace.
func (m *FileManager) checkMatchPath(filePath string) error {
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return filesystemErr("path_unavailable", "a matching path could not be resolved", contracts.ResponsibilityEnvironment, err)
	}
	if !withinDirectory(m.root, resolved) {
		return filesystemErr("path_outside_workspace", "a matching path is outside the workspace", contracts.ResponsibilityAgent, nil)
	}
	return nil
}

func formatGlobMatches(matches []string, truncated bool) string {
	if len(matches) == 0 && !truncated {
		return "no matches"
	}
	var out strings.Builder
	for _, match := range matches {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(match)
	}
	if truncated {
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString("[truncated: narrow the pattern or use offset to continue]")
	}
	return out.String()
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
	if pattern == "" {
		return "", filesystemErr("invalid_pattern", "the glob pattern must not be empty", contracts.ResponsibilityAgent, nil)
	}
	if filepath.IsAbs(pattern) || strings.ContainsRune(pattern, 0) {
		return "", filesystemErr("invalid_pattern", "the glob pattern must be workspace-relative", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(pattern, contracts.ResponsibilityAgent); err != nil {
		return "", err
	}
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

// globMatchesAny reports whether a workspace-relative path matches one of the
// patterns. A pattern without a slash matches a basename at any depth.
func globMatchesAny(patterns []string, rel string) bool {
	if len(patterns) == 0 {
		return false
	}
	base := filepath.Base(rel)
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") {
			if globMatch(pattern, rel) {
				return true
			}
			continue
		}
		if ok, err := filepath.Match(pattern, base); err == nil && ok {
			return true
		}
	}
	return false
}
