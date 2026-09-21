package actuators

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type listArguments struct {
	Path string `json:"path"`
}

func ListTool() contracts.Tool {
	return toolSchema(
		"list",
		"List directory entries, including hidden files, permissions, and sizes",
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Directory path relative to the workspace; defaults to .",
			},
		},
		nil,
	)
}

type ListEntry struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Symlink string `json:"symlink,omitempty"`
}

const maxDirectoryEntries = 500

func (m *FileManager) ListExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[listArguments](contracts.ListTool, tc)
	if err != nil {
		return "", err
	}
	if args.Path == "" {
		args.Path = "."
	}

	return contracts.RunBounded(ctx, contracts.ListTool, time.Second, func() (string, error) {
		entries, err := m.list(args.Path)
		if err != nil {
			return "", wrapToolError(contracts.ListTool, err, "list_failed", "the directory could not be listed")
		}
		return assemble(entries)
	})
}

func (m *FileManager) list(path string) ([]ListEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.resolve(path, true)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.Readdir(maxDirectoryEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > maxDirectoryEntries {
		return nil, filesystemErr("directory_too_large", "the directory contains too many entries; list a narrower directory", contracts.ResponsibilityTool, nil)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]ListEntry, 0, len(entries))
	for _, entry := range entries {
		if err := validateText(entry.Name(), contracts.ResponsibilityTool); err != nil {
			return nil, err
		}
		item := ListEntry{
			Name: entry.Name(),
			Mode: entry.Mode().String(),
			Size: entry.Size(),
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			item.Symlink, err = os.Readlink(filepath.Join(p, entry.Name()))
			if err != nil {
				return nil, err
			}
			if err := validateText(item.Symlink, contracts.ResponsibilityTool); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, nil
}
