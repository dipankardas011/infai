package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// claudeCLIPreflightTimeout bounds the two cheap probes below. Neither performs
// inference, so this only covers process start-up.
const claudeCLIPreflightTimeout = 30 * time.Second

// checkClaudeCLIAvailable reports whether this machine can actually use the
// provider: the CLI is on PATH and already signed in.
//
// It runs no model turn. `--version` proves the binary is runnable, and
// `auth status` is a local read of the CLI's own login state. Only the login
// boolean is consumed — the same output also carries an account and an
// organisation, and infai deliberately discards those.
func checkClaudeCLIAvailable(ctx context.Context, binary string) error {
	resolved, err := resolveClaudeBinary(binary)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, claudeCLIPreflightTimeout)
	defer cancel()

	if _, err := exec.CommandContext(ctx, resolved, "--version").Output(); err != nil {
		return fmt.Errorf("Claude Code CLI could not be run: %w", err)
	}

	status, err := exec.CommandContext(ctx, resolved, "auth", "status").Output()
	if err != nil {
		// A CLI that refuses to answer is not usable, but its own wording for
		// that differs by version, so report the real error rather than guess.
		return fmt.Errorf("could not read the Claude Code login status: %w", err)
	}
	var reported struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(status, &reported); err != nil {
		return fmt.Errorf("could not read the Claude Code login status: %w", err)
	}
	if reported.LoggedIn == nil {
		return errors.New("could not read the Claude Code login status: the CLI reported no login state")
	}
	if !*reported.LoggedIn {
		return claudeAuthError()
	}
	return nil
}

// resolveClaudeBinary applies the same rule as the adapter: a path is honoured
// as given, a bare name is searched on PATH.
func resolveClaudeBinary(binary string) (string, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = "claude"
	}
	if strings.ContainsRune(binary, '/') {
		return binary, nil
	}
	resolved, err := execLookPath(binary)
	if err != nil {
		return "", errors.New("Claude Code CLI not found. Install it and run `claude` once to sign in.")
	}
	return resolved, nil
}
