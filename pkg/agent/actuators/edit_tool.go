package actuators

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type editArguments struct {
	Path       string  `json:"path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all"`
}

type EditResult struct {
	Replacements int `json:"replacements"`
}

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

func (m *FileManager) EditExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[editArguments](contracts.EditTool, tc)
	if err != nil {
		return "", err
	}
	if err := editToolValidate(args); err != nil {
		return "", wrapToolError(contracts.EditTool, err, "invalid_arguments", "edit arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.EditTool, time.Second, func() (string, error) {
		n, err := m.edit(args.Path, *args.OldString, *args.NewString, args.ReplaceAll)
		if err != nil {
			return "", wrapToolError(contracts.EditTool, err, "edit_failed", "the file could not be edited")
		}
		return assemble(EditResult{Replacements: n})
	})
}

func (m *FileManager) edit(path, oldString, newString string, replaceAll bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateText(oldString, contracts.ResponsibilityAgent); err != nil {
		return 0, err
	}
	if err := validateText(newString, contracts.ResponsibilityAgent); err != nil {
		return 0, err
	}

	resolved, err := m.resolve(path, true)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return 0, filesystemErr("edit_failed", "the file could not be inspected", contracts.ResponsibilityEnvironment, err)
	case !info.Mode().IsRegular():
		return 0, filesystemErr("not_a_file", "the requested path is not a regular file", contracts.ResponsibilityAgent, nil)
	case info.Size() > maxReadBytes:
		return 0, filesystemErr("file_too_large", "the file exceeds the edit size limit", contracts.ResponsibilityTool, nil)
	}

	data, err := os.ReadFile(resolved)
	switch {
	case err != nil:
		return 0, filesystemErr("edit_failed", "the file could not be read", contracts.ResponsibilityEnvironment, err)
	case len(data) > maxReadBytes:
		return 0, filesystemErr("file_too_large", "the file exceeds the edit size limit", contracts.ResponsibilityTool, nil)
	case !utf8.Valid(data):
		return 0, filesystemErr("invalid_utf8", "the file is not valid UTF-8 text", contracts.ResponsibilityTool, nil)
	}

	if err = m.verify(resolved, data); err != nil {
		return 0, err
	}
	if err := validateText(string(data), contracts.ResponsibilityTool); err != nil {
		return 0, err
	}

	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return 0, filesystemErr("old_string_not_found", "old_string was not found in the file", contracts.ResponsibilityAgent, nil)
	}
	if count != 1 && !replaceAll {
		return 0, filesystemErr("ambiguous_edit", fmt.Sprintf("old_string matched %d times; use replace_all", count), contracts.ResponsibilityAgent, nil)
	}

	text = strings.Replace(text, oldString, newString, 1)
	if replaceAll {
		text = strings.ReplaceAll(string(data), oldString, newString)
	}
	if len(text) > maxReadBytes {
		return 0, filesystemErr("content_too_large", "the edited content exceeds the write size limit", contracts.ResponsibilityTool, nil)
	}

	mode, err := infoMode(resolved)
	if err != nil {
		return 0, filesystemErr("edit_failed", "the file permissions could not be inspected", contracts.ResponsibilityEnvironment, err)
	}

	if err := atomicWrite(resolved, []byte(text), mode); err != nil {
		return 0, filesystemErr("edit_failed", "the file could not be written", contracts.ResponsibilityEnvironment, err)
	}

	m.snapshot(resolved, []byte(text))
	return count, nil
}

func editToolValidate(args editArguments) error {
	if args.Path == "" || args.OldString == nil || args.NewString == nil {
		return contracts.NewToolExecutionError(contracts.EditTool, "invalid_arguments", "edit requires path, old_string and new_string", contracts.ResponsibilityAgent, nil)
	}
	return nil
}
