package evals

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// evalReasonLimit caps how much of what the script printed reaches the model: a
// failing build or test run can print megabytes, and this message lands in the
// sidecar's own context.
const evalReasonLimit = 2000

// NewEvalScriptHandler runs a sidecar's acceptance check. The check is graded on
// its exit status alone, but a failure is only useful to the agent that wrote it
// if it says why, so the handler returns what the script printed.
func NewEvalScriptHandler(cwd string, evalScript string) func(context.Context) error {
	const defaultEvalTimeout = time.Minute

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, defaultEvalTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "/usr/bin/env", "bash", "-s")
		cmd.Dir = cwd

		cmd.Stdin = bytes.NewBufferString(evalScript)
		cmd.WaitDelay = 5 * time.Second

		var stdout, stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stdout

		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("evaluation timed out after %s", defaultEvalTimeout)
			}
			return fmt.Errorf("evaluation failed: %s", failureReason(err, stderr.String(), stdout.String()))
		}
		return nil
	}
}

// failureReason is why the check failed, in the script's own words. The script
// decides the message by printing it — stderr first, then stdout, so a check
// that only echoes to stdout is not reduced to an exit status. A script that
// prints nothing says so, because "exit status 1" is nothing the agent can act on.
func failureReason(runErr error, stderr, stdout string) string {
	parts := make([]string, 0, 2)
	for _, out := range []string{stderr, stdout} {
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%v, and the check printed nothing; echo why it failed from the acceptance script", runErr)
	}
	return clipReason(strings.Join(parts, "\n"))
}

func clipReason(reason string) string {
	if len(reason) <= evalReasonLimit {
		return reason
	}
	return reason[:evalReasonLimit] + "\n… (output truncated)"
}
