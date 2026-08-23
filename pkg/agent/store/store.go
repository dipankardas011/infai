package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// harnessDir is the config subdirectory that holds the provider registry and
// per-session timelines.
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
