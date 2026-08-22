package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// harnessDir is the config subdirectory that holds everything the harness
// persists: provider registry (models.json) and per-session transcripts
// (sessions/<uuid>.jsonl).
const harnessDir = "infai/harness"

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

// atomicWriteJSON serializes v and writes it to path atomically (tmp + rename)
// so a crash never leaves a half-written config file behind.
func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("store: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: rename %s: %w", tmp, err)
	}
	return nil
}
