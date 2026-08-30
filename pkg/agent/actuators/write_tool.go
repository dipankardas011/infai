package actuators

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func WriteTool() contracts.Tool {
	return toolSchema(
		"write",
		"Write a UTF-8 text file. Existing files must have been read first.",
		map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path relative to the workspace",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Complete UTF-8 file contents",
			},
		},
		[]string{"path", "content"},
	)
}

func (m *FileManager) Write(path, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateText(content, ResponsibilityAgent); err != nil {
		return err
	}
	if len(content) > maxReadBytes {
		return filesystemErr("content_too_large", "the content exceeds the write size limit", ResponsibilityTool, nil)
	}
	resolved, err := m.resolve(path, false)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	info, err := os.Stat(resolved)
	if os.IsNotExist(err) {
		// A missing final component is the only valid new-file case.
	} else if err != nil {
		return filesystemErr("write_failed", "the existing file could not be inspected", ResponsibilityEnvironment, err)
	} else {
		if !info.Mode().IsRegular() {
			return filesystemErr("not_a_file", "the requested path is not a regular file", ResponsibilityAgent, nil)
		}
		if info.Size() > maxReadBytes {
			return filesystemErr("file_too_large", "the existing file exceeds the write size limit", ResponsibilityTool, nil)
		}
		old, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return filesystemErr("write_failed", "the existing file could not be read", ResponsibilityEnvironment, readErr)
		}
		if len(old) > maxReadBytes {
			return filesystemErr("file_too_large", "the existing file exceeds the write size limit", ResponsibilityTool, nil)
		}
		if err = m.verify(resolved, old); err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	data := []byte(content)
	if err := atomicWrite(resolved, data, mode); err != nil {
		return filesystemErr("write_failed", "the file could not be written", ResponsibilityEnvironment, err)
	}
	m.snapshot(resolved, data)
	return nil
}

func writeExecution(ctx context.Context) (string, error) {
	var args struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	_, err := decodeArgs(ctx, &args)
	if err != nil || args.Path == "" || args.Content == nil {
		return "", filesystemErr("invalid_arguments", "write requires path and content", ResponsibilityAgent, err)
	}
	m, err := fileManager(ctx)
	if err != nil {
		return "", err
	}
	if err := m.Write(args.Path, *args.Content); err != nil {
		return "", err
	}
	return "written", nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".infai-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func infoMode(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}
