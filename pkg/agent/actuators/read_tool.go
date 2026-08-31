package actuators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	MIMEType      string `json:"mime_type"`
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
	var args readArguments
	if _, err := decodeArgs(ctx, &args); err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.ReadTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.ReadTool, "invalid_arguments", "read arguments could not be decoded", ResponsibilityAgent, err)
	}
	if err := readToolValidate(args); err != nil {
		return "", err
	}

	m := FileManagerFromContext(ctx)
	if m == nil {
		return "", execErr(contracts.ReadTool, "missing_file_manager", "a file manager is required to read files", ResponsibilitySession, nil)
	}

	out, err := m.Read(args.Path, args.Offset, args.Limit, args.Metadata)
	if err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.ReadTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.ReadTool, "read_failed", "the file could not be read", ResponsibilityTool, err)
	}

	return out, nil
}

func readToolValidate(args readArguments) error {
	if args.Path == "" {
		return execErr(contracts.ReadTool, "invalid_arguments", "read requires a non-empty relative path", ResponsibilityAgent, nil)
	}
	if (args.Offset != nil && *args.Offset < 1) || (args.Limit != nil && *args.Limit < 1) {
		return execErr(contracts.ReadTool, "invalid_arguments", "offset and limit must be positive line numbers", ResponsibilityAgent, nil)
	}
	if args.Metadata && (args.Offset != nil || args.Limit != nil) {
		return execErr(contracts.ReadTool, "invalid_arguments", "metadata cannot be combined with offset or limit", ResponsibilityAgent, nil)
	}

	return nil
}

func (m *FileManager) Read(path string, offset, limit *int, metadata bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	resolved, err := m.resolve(path, true)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return "", filesystemErr("file_unavailable", "the file could not be inspected", ResponsibilityEnvironment, err)
	case !info.Mode().IsRegular():
		return "", filesystemErr("not_a_file", "the requested path is not a regular file", ResponsibilityAgent, nil)
	case info.Size() > maxReadBytes:
		return "", filesystemErr("file_too_large", "the file exceeds the read size limit", ResponsibilityTool, nil)
	}

	data, err := os.ReadFile(resolved)
	switch {
	case err != nil:
		return "", filesystemErr("read_failed", "the file could not be read", ResponsibilityEnvironment, err)
	case len(data) > maxReadBytes:
		return "", filesystemErr("file_too_large", "the file exceeds the read size limit", ResponsibilityTool, nil)

	case !utf8.Valid(data):
		return "", filesystemErr("invalid_utf8", "the file is not valid UTF-8 text", ResponsibilityTool, nil)

	default:
		if err := validateText(string(data), ResponsibilityTool); err != nil {
			return "", err
		}
	}

	if metadata {
		return readMetadataJSON(path, resolved, info, data)
	}

	m.snapshot(resolved, data)
	lines := splitLines(data)
	start := 0
	if offset != nil {
		start = *offset - 1
	}
	if start >= len(lines) {
		return "", nil
	}
	end := len(lines)
	if limit != nil && *limit < end-start {
		end = start + *limit
	}

	return strings.Join(lines[start:end], ""), nil
}

func readMetadataJSON(path, resolved string, info os.FileInfo, data []byte) (string, error) {
	extension := filepath.Ext(resolved)
	format := strings.TrimPrefix(extension, ".")
	if format == "" {
		format = "unknown"
	}
	metadata := readMetadata{
		Path:          path,
		Location:      resolved,
		Permissions:   fmt.Sprintf("%#o", info.Mode().Perm()),
		Mode:          info.Mode().String(),
		SizeBytes:     info.Size(),
		DiskSizeBytes: diskSizeBytes(info),
		Format:        format,
		MIMEType:      mime.TypeByExtension(extension),
		UTF8:          utf8.Valid(data),
	}
	if metadata.UTF8 {
		n := len(splitLines(data))
		metadata.LineCount = &n
	}
	b, err := json.Marshal(metadata)
	return string(b), err
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
	return err == nil && filepath.IsLocal(relative)
}
