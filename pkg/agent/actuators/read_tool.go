package actuators

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const maxReadBytes = 1 << 20

type readArguments struct {
	Path     string `json:"path"`
	Offset   *int   `json:"offset,omitempty"`
	Limit    *int   `json:"limit,omitempty"`
	Metadata bool   `json:"metadata,omitempty"`
}

type readMetadata struct {
	Path          string `json:"path"`
	Location      string `json:"location"`
	Permissions   string `json:"permissions"`
	Mode          string `json:"mode"`
	SizeBytes     int64  `json:"size_bytes"`
	DiskSizeBytes int64  `json:"disk_size_bytes"`
	Format        string `json:"format"`
	UTF8          bool   `json:"utf8"`
	LineCount     *int   `json:"line_count,omitempty"`
}

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
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based starting line number",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return",
				},
				"metadata": map[string]any{
					"type":        "boolean",
					"description": "Return file metadata instead of file contents",
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

	var args readArguments
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
	if args.Offset != nil && *args.Offset < 1 || args.Limit != nil && *args.Limit < 1 {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_arguments",
			Reason:         "offset and limit must be positive line numbers",
			Responsibility: ResponsibilityAgent,
		}
	}
	if args.Metadata && (args.Offset != nil || args.Limit != nil) {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_arguments",
			Reason:         "metadata cannot be combined with offset or limit",
			Responsibility: ResponsibilityAgent,
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
	validUTF8 := utf8.Valid(data)
	if args.Metadata {
		format := filepath.Ext(resolved)
		if format == "" {
			format = mime.TypeByExtension(format)
		}
		if format == "" {
			format = "unknown"
		}
		metadata := readMetadata{
			Path:          args.Path,
			Location:      resolved,
			Permissions:   fmt.Sprintf("%#o", info.Mode().Perm()),
			Mode:          info.Mode().String(),
			SizeBytes:     info.Size(),
			DiskSizeBytes: diskSizeBytes(info),
			Format:        format,
			UTF8:          validUTF8,
		}
		if validUTF8 {
			lines := splitLines(data)
			count := len(lines)
			metadata.LineCount = &count
		}
		output, err := json.Marshal(metadata)
		if err != nil {
			return "", readEnvironmentError("metadata_failed", "file metadata could not be encoded", err)
		}
		return string(output), nil
	}
	if !validUTF8 {
		return "", &ExecutionError{
			Tool:           string(contracts.ReadTool),
			Code:           "invalid_utf8",
			Reason:         "the file is not valid UTF-8 text",
			Responsibility: ResponsibilityTool,
		}
	}
	lines := splitLines(data)
	start := 0
	if args.Offset != nil {
		start = *args.Offset - 1
	}
	if start >= len(lines) {
		return "", nil
	}
	end := len(lines)
	if args.Limit != nil && start+*args.Limit < end {
		end = start + *args.Limit
	}
	return strings.Join(lines[start:end], ""), nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
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
