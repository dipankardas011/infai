package evals

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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
		cmd.Stdout = &stdout // TODO: for now I am not sure what we will do

		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("evaluation timed out after %s", defaultEvalTimeout)
			}
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("evaluation failed: %s", message)
		}
		return nil
	}

}
