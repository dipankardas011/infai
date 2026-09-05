package actuators

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func SearchTool() contracts.Tool {
	return toolSchema(
		"search",
		"Search UTF-8 file contents and return matching paths, line numbers, and text",
		map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Literal text to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory relative to the workspace; defaults to .",
			},
		},
		[]string{"pattern"},
	)
}

type searchArguments struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type SearchResult struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

const maxSearchOutputBytes = maxToolContentBytes

func searchExecution(ctx context.Context) (string, error) {
	var args searchArguments
	if _, err := decodeArgs(ctx, &args); err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.SearchTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.SearchTool, "invalid_arguments", "search arguments could not be decoded", ResponsibilityAgent, err)
	}
	if args.Path == "" {
		args.Path = "."
	}
	if err := searchToolValidate(args); err != nil {
		return "", err
	}
	m := FileManagerFromContext(ctx)
	if m == nil {
		return "", execErr(contracts.SearchTool, "missing_file_manager", "a file manager is required to search files", ResponsibilitySession, nil)
	}
	results, err := m.Search(args.Pattern, args.Path)
	if err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.SearchTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.SearchTool, "search_failed", "the search could not be completed", ResponsibilityTool, err)
	}
	return assemble(results)
}

func (m *FileManager) Search(pattern, path string) ([]SearchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	root, err := m.resolve(path, true)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0)
	outputBytes := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxReadBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return filesystemErr("search_failed", "a file could not be read during search", ResponsibilityEnvironment, err)
		}
		if len(data) > maxReadBytes {
			return nil
		}
		if !utf8.Valid(data) {
			return nil
		}
		relative, err := filepath.Rel(m.root, path)
		if err != nil {
			return err
		}
		if err := validateText(relative, ResponsibilityTool); err != nil {
			return err
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, pattern) {
				if err := validateText(line, ResponsibilityTool); err != nil {
					return err
				}
				outputBytes += len(line)
				if len(results) >= maxDirectoryEntries || outputBytes > maxSearchOutputBytes {
					return filesystemErr("search_too_large", "the search produced too much output; narrow the path or pattern", ResponsibilityTool, nil)
				}
				results = append(results, SearchResult{
					Path: filepath.ToSlash(relative),
					Line: lineNumber + 1,
					Text: line,
				})
			}
		}
		return nil
	})
	return results, err
}

func searchToolValidate(args searchArguments) error {
	if args.Pattern == "" {
		return execErr(contracts.SearchTool, "invalid_arguments", "search requires pattern", ResponsibilityAgent, nil)
	}

	if err := validateText(args.Pattern, ResponsibilityAgent); err != nil {
		return err
	}

	return nil
}
