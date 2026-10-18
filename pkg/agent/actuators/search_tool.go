package actuators

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const (
	searchOutputContent = "content"
	searchOutputFiles   = "files"
	searchOutputCount   = "count"

	searchDefaultLimit = 200
	searchMaxLimit     = 1000
	searchMaxContext   = 20
	searchMaxPattern   = 4096
)

func SearchTool() contracts.Tool {
	return toolSchema(
		"search",
		"Search UTF-8 file contents. Literal by default; set regex=true for a regular expression. Skips hidden paths and .git unless include_hidden is true. Returns path:line:text (content), one path per line (files), or path:count (count). A trailing [truncated: ...] line means more results exist.",
		map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Text or regular expression to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory relative to the workspace; defaults to .",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "Treat pattern as a Go regular expression instead of literal text",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "Match without regard to case",
			},
			"include": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Only search files whose workspace-relative path matches one of these globs; a glob without a slash matches a basename at any depth",
			},
			"exclude": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Skip files or directories whose workspace-relative path matches one of these globs",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Lines of context before and after each match; output=content only",
			},
			"output": map[string]any{
				"type":        "string",
				"enum":        []string{searchOutputContent, searchOutputFiles, searchOutputCount},
				"description": "content: path:line:text; files: matching paths; count: matching lines per file",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum matches or files to return; defaults to %d", searchDefaultLimit),
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Number of matches or files to skip; use with limit to page",
			},
			"include_hidden": map[string]any{
				"type":        "boolean",
				"description": "Search dotfiles and dot-directories; .git is always skipped",
			},
		},
		[]string{"pattern"},
	)
}

type searchArguments struct {
	Pattern         string   `json:"pattern"`
	Path            string   `json:"path"`
	Regex           bool     `json:"regex"`
	CaseInsensitive bool     `json:"case_insensitive"`
	Include         []string `json:"include"`
	Exclude         []string `json:"exclude"`
	Context         int      `json:"context"`
	Output          string   `json:"output"`
	Limit           int      `json:"limit"`
	Offset          int      `json:"offset"`
	IncludeHidden   bool     `json:"include_hidden"`
}

type lineMatcher func(string) bool

func (m *FileManager) SearchExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[searchArguments](contracts.SearchTool, tc)
	if err != nil {
		return "", err
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.Output == "" {
		args.Output = searchOutputContent
	}
	if args.Limit == 0 {
		args.Limit = searchDefaultLimit
	}
	if err := searchToolValidate(&args); err != nil {
		return "", wrapToolError(contracts.SearchTool, err, "invalid_arguments", "search arguments are invalid")
	}
	matchLine, err := compileSearchMatcher(args)
	if err != nil {
		return "", wrapToolError(contracts.SearchTool, err, "invalid_arguments", "search arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.SearchTool, 10*time.Second, func() (string, error) {
		output, err := m.search(args, matchLine)
		if err != nil {
			return "", wrapToolError(contracts.SearchTool, err, "search_failed", "the search could not be completed")
		}
		return output, nil
	})
}

func searchToolValidate(args *searchArguments) error {
	if args.Pattern == "" {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", "search requires pattern", contracts.ResponsibilityAgent, nil)
	}
	if len(args.Pattern) > searchMaxPattern {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", "the search pattern is too long", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(args.Pattern, contracts.ResponsibilityAgent); err != nil {
		return err
	}
	switch args.Output {
	case searchOutputContent, searchOutputFiles, searchOutputCount:
	default:
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", "output must be content, files, or count", contracts.ResponsibilityAgent, nil)
	}
	if args.Limit < 1 || args.Limit > searchMaxLimit {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", fmt.Sprintf("limit must be between 1 and %d", searchMaxLimit), contracts.ResponsibilityAgent, nil)
	}
	if args.Offset < 0 {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", "offset must not be negative", contracts.ResponsibilityAgent, nil)
	}
	if args.Context < 0 || args.Context > searchMaxContext {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", fmt.Sprintf("context must be between 0 and %d", searchMaxContext), contracts.ResponsibilityAgent, nil)
	}
	if args.Output != searchOutputContent && args.Context != 0 {
		return contracts.NewToolExecutionError(contracts.SearchTool, "invalid_arguments", "context is only used with output=content", contracts.ResponsibilityAgent, nil)
	}
	for index, pattern := range args.Include {
		normalized, err := normalizeGlobPattern(pattern)
		if err != nil {
			return err
		}
		args.Include[index] = normalized
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

// compileSearchMatcher uses strings.Contains for the common literal,
// case-sensitive case and a compiled expression for everything else.
func compileSearchMatcher(args searchArguments) (lineMatcher, error) {
	if !args.Regex && !args.CaseInsensitive {
		pattern := args.Pattern
		return func(line string) bool { return strings.Contains(line, pattern) }, nil
	}
	expression := args.Pattern
	if !args.Regex {
		expression = regexp.QuoteMeta(expression)
	}
	if args.CaseInsensitive {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return nil, contracts.NewToolExecutionError(
			contracts.SearchTool,
			"invalid_arguments",
			"search pattern is not a valid regular expression: "+err.Error(),
			contracts.ResponsibilityAgent,
			err,
		)
	}
	return compiled.MatchString, nil
}

func (m *FileManager) search(args searchArguments, matchLine lineMatcher) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	root, err := m.resolve(args.Path, true)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	seen := 0
	written := 0
	truncated := false
	byteLimited := false

	emit := func(text string) bool {
		added := len(text)
		if out.Len() > 0 {
			added++
		}
		if out.Len()+added > maxToolContentBytes {
			byteLimited = true
			return false
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(text)
		return true
	}

	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
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
		isRoot := filePath == root

		if entry.IsDir() {
			if isRoot {
				return nil
			}
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if !args.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if globMatchesAny(args.Exclude, rel) {
				return filepath.SkipDir
			}
			return nil
		}

		if !isRoot {
			if !args.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return nil
			}
			if len(args.Include) > 0 && !globMatchesAny(args.Include, rel) {
				return nil
			}
			if globMatchesAny(args.Exclude, rel) {
				return nil
			}
		}
		if err := validateText(rel, contracts.ResponsibilityTool); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxReadBytes {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return filesystemErr("search_failed", "a file could not be read during search", contracts.ResponsibilityEnvironment, err)
		}
		if !utf8.Valid(data) {
			return nil
		}

		lines := searchLines(data)
		switch args.Output {
		case searchOutputFiles:
			for _, line := range lines {
				if !matchLine(strings.TrimSuffix(line, "\r")) {
					continue
				}
				seen++
				if seen <= args.Offset {
					break
				}
				if written >= args.Limit {
					truncated = true
					return filepath.SkipAll
				}
				if !emit(rel) {
					truncated = true
					return filepath.SkipAll
				}
				written++
				break
			}

		case searchOutputCount:
			count := 0
			for _, line := range lines {
				if matchLine(strings.TrimSuffix(line, "\r")) {
					count++
				}
			}
			if count == 0 {
				return nil
			}
			seen++
			if seen <= args.Offset {
				return nil
			}
			if written >= args.Limit {
				truncated = true
				return filepath.SkipAll
			}
			if !emit(fmt.Sprintf("%s:%d", rel, count)) {
				truncated = true
				return filepath.SkipAll
			}
			written++

		case searchOutputContent:
			matches := make([]int, 0)
			for index, line := range lines {
				if matchLine(strings.TrimSuffix(line, "\r")) {
					matches = append(matches, index)
				}
			}
			if len(matches) == 0 {
				return nil
			}

			matched := make(map[int]bool, len(matches))
			for _, index := range matches {
				matched[index] = true
			}
			lastEnd := -1
			for _, index := range matches {
				seen++
				if seen <= args.Offset {
					continue
				}
				if written >= args.Limit {
					truncated = true
					return filepath.SkipAll
				}

				start := max(0, index-args.Context)
				end := min(len(lines)-1, index+args.Context)
				if args.Context > 0 && lastEnd >= 0 && start > lastEnd+1 {
					if !emit("--") {
						truncated = true
						return filepath.SkipAll
					}
				}
				if start <= lastEnd {
					start = lastEnd + 1
				}
				for lineIndex := start; lineIndex <= end; lineIndex++ {
					text := strings.TrimSuffix(lines[lineIndex], "\r")
					if err := validateText(text, contracts.ResponsibilityTool); err != nil {
						return err
					}
					prefix := fmt.Sprintf("%s-%d-", rel, lineIndex+1)
					if matched[lineIndex] {
						prefix = fmt.Sprintf("%s:%d:", rel, lineIndex+1)
					}
					if !emit(prefix + text) {
						truncated = true
						return filepath.SkipAll
					}
				}
				if end > lastEnd {
					lastEnd = end
				}
				written++
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if out.Len() == 0 && !truncated {
		return "no matches", nil
	}
	if truncated {
		marker := fmt.Sprintf("[truncated: use offset=%d to continue]", args.Offset+args.Limit)
		if byteLimited {
			marker = "[truncated: output limit reached; narrow the pattern or path]"
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(marker)
	}
	return out.String(), nil
}

func searchLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
