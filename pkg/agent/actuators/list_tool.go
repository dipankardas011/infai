package actuators

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

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

const maxDirectoryEntries = 10_000

func (m *FileManager) List(path string) ([]ListEntry, error) {
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
		return nil, filesystemErr("directory_too_large", "the directory contains too many entries", ResponsibilityTool, nil)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]ListEntry, 0, len(entries))
	for _, entry := range entries {
		if err := validateText(entry.Name(), ResponsibilityTool); err != nil {
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
			if err := validateText(item.Symlink, ResponsibilityTool); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func listExecution(ctx context.Context) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	_, err := decodeArgs(ctx, &args)
	if err != nil {
		return "", err
	}
	if args.Path == "" {
		args.Path = "."
	}
	m, err := fileManager(ctx)
	if err != nil {
		return "", err
	}
	entries, err := m.List(args.Path)
	output, marshalErr := mustJSON(entries)
	if err == nil {
		err = marshalErr
	}
	return string(output), err
}
