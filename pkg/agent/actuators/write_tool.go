package actuators

import (
	"context"
	"os"
	"path/filepath"
	"time"

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

func (m *FileManager) WriteExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[writeArguments](contracts.WriteTool, tc)
	if err != nil {
		return "", err
	}

	if err := writeToolValidate(args); err != nil {
		return "", wrapToolError(contracts.WriteTool, err, "invalid_arguments", "write arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.WriteTool, time.Second, func() (string, error) {
		if err := m.write(args.Path, *args.Content); err != nil {
			return "", wrapToolError(contracts.WriteTool, err, "write_failed", "the file could not be written")
		}
		return assemble(WriteResult{Status: "written"})
	})
}

func (m *FileManager) write(path, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	resolved, err := m.resolve(path, false)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	info, statErr := os.Stat(resolved)
	if statErr != nil && !os.IsNotExist(statErr) {
		return filesystemErr("write_failed", "the existing file could not be inspected", contracts.ResponsibilityEnvironment, statErr)
	}
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return filesystemErr("not_a_file", "the requested path is not a regular file", contracts.ResponsibilityAgent, nil)
		}
		old, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return filesystemErr("write_failed", "the existing file could not be read", contracts.ResponsibilityEnvironment, readErr)
		}
		if info.Size() > maxReadBytes {
			return filesystemErr("file_too_large", "the existing file exceeds the write size limit", contracts.ResponsibilityTool, nil)
		}
		if len(old) > maxReadBytes {
			return filesystemErr("file_too_large", "the existing file exceeds the write size limit", contracts.ResponsibilityTool, nil)
		}
		mode = info.Mode().Perm()

		// A file on disk must have been read first and not changed since.
		// A brand-new file never existed, so there is nothing to verify.
		if err = m.verify(resolved, old); err != nil {
			return err
		}
	}

	if err := atomicWrite(resolved, []byte(content), mode); err != nil {
		return filesystemErr("write_failed", "the file could not be written", contracts.ResponsibilityEnvironment, err)
	}

	m.snapshot(resolved, []byte(content))
	return nil
}

func writeToolValidate(args writeArguments) error {
	if args.Path == "" || args.Content == nil {
		return contracts.NewToolExecutionError(contracts.WriteTool, "invalid_arguments", "write requires path and content", contracts.ResponsibilityAgent, nil)
	}

	if err := validateText(*args.Content, contracts.ResponsibilityAgent); err != nil {
		return err
	}

	if len(*args.Content) > maxReadBytes {
		return filesystemErr("content_too_large", "the content exceeds the write size limit", contracts.ResponsibilityTool, nil)
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
