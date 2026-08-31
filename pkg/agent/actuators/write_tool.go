package actuators

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type writeArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

type WriteResult struct {
	Status string `json:"status"`
}

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

func writeExecution(ctx context.Context) (string, error) {
	var args writeArguments
	if _, err := decodeArgs(ctx, &args); err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.WriteTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.WriteTool, "invalid_arguments", "write arguments could not be decoded", ResponsibilityAgent, err)
	}
	if err := writeToolValidate(args); err != nil {
		return "", err
	}
	m := FileManagerFromContext(ctx)
	if m == nil {
		return "", execErr(contracts.WriteTool, "missing_file_manager", "a file manager is required to write files", ResponsibilitySession, nil)
	}
	if err := m.Write(args.Path, *args.Content); err != nil {
		if fileErr, ok := errors.AsType[*filesystemError](err); ok {
			return "", execErr(contracts.WriteTool, fileErr.code, fileErr.reason, fileErr.responsibility, err)
		}
		return "", execErr(contracts.WriteTool, "write_failed", "the file could not be written", ResponsibilityTool, err)
	}
	return assemble(WriteResult{Status: "written"})
}

func (m *FileManager) Write(path, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	resolved, err := m.resolve(path, false)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	info, err := os.Stat(resolved)
	old, readErr := os.ReadFile(resolved)

	switch {
	case err != nil && !os.IsNotExist(err):
		return filesystemErr("write_failed", "the existing file could not be inspected", ResponsibilityEnvironment, err)
	case readErr != nil && !os.IsNotExist(readErr):
		return filesystemErr("write_failed", "the existing file could not be read", ResponsibilityEnvironment, readErr)
	case os.IsNotExist(err):
		// A missing final component is the only valid new-file case.
	case !info.Mode().IsRegular():
		return filesystemErr("not_a_file", "the requested path is not a regular file", ResponsibilityAgent, nil)
	case info.Size() > maxReadBytes:
		return filesystemErr("file_too_large", "the existing file exceeds the write size limit", ResponsibilityTool, nil)
	case len(old) > maxReadBytes:
		return filesystemErr("file_too_large", "the existing file exceeds the write size limit", ResponsibilityTool, nil)
	}
	mode = info.Mode().Perm()

	if err = m.verify(resolved, old); err != nil {
		return err
	}

	if err := atomicWrite(resolved, []byte(content), mode); err != nil {
		return filesystemErr("write_failed", "the file could not be written", ResponsibilityEnvironment, err)
	}

	m.snapshot(resolved, []byte(content))
	return nil
}

func writeToolValidate(args writeArguments) error {
	if args.Path == "" || args.Content == nil {
		return execErr(contracts.WriteTool, "invalid_arguments", "write requires path and content", ResponsibilityAgent, nil)
	}

	if err := validateText(*args.Content, ResponsibilityAgent); err != nil {
		return err
	}

	if len(*args.Content) > maxReadBytes {
		return filesystemErr("content_too_large", "the content exceeds the write size limit", ResponsibilityTool, nil)
	}

	return nil
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
