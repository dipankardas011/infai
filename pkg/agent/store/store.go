package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// harnessDir is the config subdirectory that holds the provider registry and
// per-session timelines.
const harnessDir = "infai/harness"

// toolsFileName is the MCP server registry inside harnessDir.
const toolsFileName = "tools.json"

func Root() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config dir: %w", err)
	}
	return filepath.Join(cfg, harnessDir), nil
}

func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to mkdir -p %s: %w", dir, err)
	}
	return nil
}

// atomicWrite replaces path with data by writing a sibling temp file and
// renaming it over the target, so a failed write never leaves a partial file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".infai-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
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
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("commit %s: %w", filepath.Base(path), err)
	}
	return nil
}
