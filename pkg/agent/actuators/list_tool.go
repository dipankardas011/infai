package actuators

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

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

const maxDirectoryEntries = 10_000

func listExecution(ctx context.Context) (string, error) {
	var args listArguments
	if _, err := decodeArgs(ctx, &args); err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.ListTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.ListTool, "invalid_arguments", "list arguments could not be decoded", ResponsibilityAgent, err)
	}
	if args.Path == "" {
		args.Path = "."
	}

	m := FileManagerFromContext(ctx)
	if m == nil {
		return "", execErr(contracts.ListTool, "missing_file_manager", "a file manager is required to list directories", ResponsibilitySession, nil)
	}

	entries, err := m.List(args.Path)
	if err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.ListTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.ListTool, "list_failed", "the directory could not be listed", ResponsibilityTool, err)
	}
	return assemble(entries)
}

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
