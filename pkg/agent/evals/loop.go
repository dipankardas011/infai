package evals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// evalReasonLimit caps how much of what the script printed reaches the model:
	// a failing build or test run can print megabytes, and this message lands in
	// the sidecar's own context.
	evalReasonLimit    = 2000
	defaultEvalTimeout = 10 * time.Minute
)

// NewEvalScriptHandler runs a sidecar's acceptance check. The check is graded on
// its exit status alone, but a failure is only useful to the agent that wrote it
// if it says why, so the handler returns what the script printed.
func NewEvalScriptHandler(cwd string, evalScript string) func(context.Context) error {
	return func(ctx context.Context) error {
		timeoutCause := fmt.Errorf("evaluation timed out after %s", defaultEvalTimeout)
		ctx, cancel := context.WithTimeoutCause(ctx, defaultEvalTimeout, timeoutCause)
		defer cancel()

		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", "-s")
		cmd.Dir = cwd

		cmd.Stdin = bytes.NewBufferString(evalScript)
		cmd.WaitDelay = 5 * time.Second

		var stdout, stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stdout

		if err := cmd.Run(); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				if errors.Is(cause, timeoutCause) {
					return timeoutCause
				}
				return cause
			}
			return fmt.Errorf("evaluation failed: %s", failureReason(err, stderr.String(), stdout.String()))
		}
		return nil
	}
}

// failureReason reports diagnostics emitted by the caller-provided acceptance
// script. stderr comes first, then stdout. If neither stream has output, it
// explicitly says no failure details are available.
func failureReason(runErr error, stderr, stdout string) string {
	parts := make([]string, 0, 2)
	for _, stream := range []struct {
		label  string
		output string
	}{
		{label: "stderr", output: stderr},
		{label: "stdout", output: stdout},
	} {
		if trimmed := strings.TrimSpace(stream.output); trimmed != "" {
			parts = append(parts, fmt.Sprintf("%s:\n%s", stream.label, trimmed))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%v; the acceptance script printed nothing, so no failure details are available", runErr)
	}
	return clipReason(strings.Join(parts, "\n"))
}

func clipReason(reason string) string {
	if len(reason) <= evalReasonLimit {
		return reason
	}
	return reason[:evalReasonLimit] + "\n… (output truncated)"
}
