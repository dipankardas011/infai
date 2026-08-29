package actuators

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const maxReadBytes = 1 << 20

func ReadTool() contracts.Tool {
	return contracts.Tool{
		Name:        "read",
		Description: "Read a UTF-8 text file",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to the workspace",
				},
			},
			RequiredFields:       []string{"path"},
			AdditionalProperties: false,
		},
	}
}

func readExecution(ctx context.Context) (string, error) {
	workspace, ok := WorkspaceFromContext(ctx)
	if !ok {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "missing_workspace",
			Reason:         "a workspace is required to read files",
			Responsibility: ResponsibilitySession,
		}
	}
	call, ok := ToolCallFromContext(ctx)
	if !ok {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "missing_tool_call",
			Reason:         "the read request is missing its tool arguments",
			Responsibility: ResponsibilitySession,
		}
	}

	var args struct {
		Path string `json:"path"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil || args.Path == "" {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_arguments",
			Reason:         "read requires a non-empty relative path",
			Responsibility: ResponsibilityAgent,
			cause:          err,
		}
	}
	if filepath.IsAbs(args.Path) || strings.ContainsRune(args.Path, 0) {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_path",
			Reason:         "the path must be relative to the workspace",
			Responsibility: ResponsibilityAgent,
		}
	}

	root, err := filepath.Abs(workspace.Root)
	if err != nil {
		return "", readEnvironmentError("invalid_workspace", "the workspace is invalid", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", readEnvironmentError("workspace_unavailable", "the workspace is unavailable", err)
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.Clean(args.Path)))
	if err != nil {
		return "", readEnvironmentError("invalid_path", "the path could not be resolved", err)
	}
	if !withinDirectory(root, candidate) {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "path_outside_workspace",
			Reason:         "the path must remain inside the workspace",
			Responsibility: ResponsibilityAgent,
		}
	}

	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", readEnvironmentError("file_unavailable", "the file could not be opened", err)
	}
	if !withinDirectory(root, resolved) {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "symlink_outside_workspace",
			Reason:         "the file must remain inside the workspace",
			Responsibility: ResponsibilityAgent,
		}
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", readEnvironmentError("file_unavailable", "the file could not be inspected", err)
	}
	if !info.Mode().IsRegular() {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "not_a_file",
			Reason:         "the requested path is not a regular file",
			Responsibility: ResponsibilityAgent,
		}
	}
	if info.Size() > maxReadBytes {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "file_too_large",
			Reason:         "the file exceeds the read size limit",
			Responsibility: ResponsibilityTool,
		}
	}

	file, err := os.Open(resolved)
	if err != nil {
		return "", readEnvironmentError("file_unavailable", "the file could not be opened", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReadBytes+1))
	if err != nil {
		return "", readEnvironmentError("read_failed", "the file could not be read", err)
	}
	if len(data) > maxReadBytes {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "file_too_large",
			Reason:         "the file exceeds the read size limit",
			Responsibility: ResponsibilityTool,
		}
	}
	if !utf8.Valid(data) {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_utf8",
			Reason:         "the file is not valid UTF-8 text",
			Responsibility: ResponsibilityTool,
		}
	}
	return string(data), nil
}

func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readEnvironmentError(code, reason string, cause error) error {
	return &ExecutionError{
		Tool:           string(contracts.ReadTool),
		Code:           code,
		Reason:         reason,
		Responsibility: ResponsibilityEnvironment,
		cause:          cause,
	}
}
