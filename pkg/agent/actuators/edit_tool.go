package actuators

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func EditTool() contracts.Tool {
	return toolSchema(
		"edit",
		"Replace exact text in a UTF-8 file. The file must have been read first.",
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path relative to the workspace",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Exact text to replace",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace every match; default is false",
			},
		},
		[]string{"path", "old_string", "new_string"},
	)
}

func (m *FileManager) Edit(path, oldString, newString string, replaceAll bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateText(oldString, ResponsibilityAgent); err != nil {
		return 0, err
	}
	if err := validateText(newString, ResponsibilityAgent); err != nil {
		return 0, err
	}

	resolved, err := m.resolve(path, true)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return 0, filesystemErr("edit_failed", "the file could not be inspected", ResponsibilityEnvironment, err)
	}
	if !info.Mode().IsRegular() {
		return 0, filesystemErr("not_a_file", "the requested path is not a regular file", ResponsibilityAgent, nil)
	}
	if info.Size() > maxReadBytes {
		return 0, filesystemErr("file_too_large", "the file exceeds the edit size limit", ResponsibilityTool, nil)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return 0, filesystemErr("edit_failed", "the file could not be read", ResponsibilityEnvironment, err)
	}
	if len(data) > maxReadBytes {
		return 0, filesystemErr("file_too_large", "the file exceeds the edit size limit", ResponsibilityTool, nil)
	}
	if err = m.verify(resolved, data); err != nil {
		return 0, err
	}
	if !utf8.Valid(data) {
		return 0, filesystemErr("invalid_utf8", "the file is not valid UTF-8 text", ResponsibilityTool, nil)
	}
	if err := validateText(string(data), ResponsibilityTool); err != nil {
		return 0, err
	}
	if oldString == "" {
		return 0, filesystemErr("invalid_arguments", "old_string must not be empty", ResponsibilityAgent, nil)
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return 0, filesystemErr("old_string_not_found", "old_string was not found in the file", ResponsibilityAgent, nil)
	}
	if count != 1 && !replaceAll {
		return 0, filesystemErr("ambiguous_edit", fmt.Sprintf("old_string matched %d times; use replace_all", count), ResponsibilityAgent, nil)
	}
	text = strings.Replace(text, oldString, newString, 1)
	if replaceAll {
		text = strings.ReplaceAll(string(data), oldString, newString)
	}
	if len(text) > maxReadBytes {
		return 0, filesystemErr("content_too_large", "the edited content exceeds the write size limit", ResponsibilityTool, nil)
	}
	mode, err := infoMode(resolved)
	if err != nil {
		return 0, filesystemErr("edit_failed", "the file permissions could not be inspected", ResponsibilityEnvironment, err)
	}
	if err := atomicWrite(resolved, []byte(text), mode); err != nil {
		return 0, filesystemErr("edit_failed", "the file could not be written", ResponsibilityEnvironment, err)
	}
	m.snapshot(resolved, []byte(text))
	return count, nil
}

func editExecution(ctx context.Context) (string, error) {
	var args struct {
		Path       string  `json:"path"`
		OldString  *string `json:"old_string"`
		NewString  *string `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
	}
	_, err := decodeArgs(ctx, &args)
	if err != nil || args.Path == "" || args.OldString == nil || args.NewString == nil {
		return "", filesystemErr("invalid_arguments", "edit requires path, old_string and new_string", ResponsibilityAgent, err)
	}
	m, err := fileManager(ctx)
	if err != nil {
		return "", err
	}
	n, err := m.Edit(args.Path, *args.OldString, *args.NewString, args.ReplaceAll)
	if err != nil {
		return "", err
	}
	output, err := mustJSON(map[string]any{"replacements": n})
	if err != nil {
		return "", err
	}
	return string(output), nil
}
