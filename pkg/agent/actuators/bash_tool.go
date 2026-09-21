package actuators

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// defaultBashTimeout caps a single command when the caller does not set one.
// maxBashTimeout bounds what a caller may request. A runaway process is a known
// failure mode for arbitrary shell — the deadline kills the whole process group.
const (
	defaultBashTimeout = 2 * time.Minute
	maxBashTimeout     = 15 * time.Minute
)

type bashArguments struct {
	Command string `json:"command"`
	Workdir string `json:"workdir"`
	Timeout *int   `json:"timeout,omitempty"`
}

type BashResult struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

func BashTool() contracts.Tool {
	return toolSchema(
		"bash",
		"Run a shell command in the workspace and return its output. Every command is validated against a dangerous-operation blocklist and reviewed by a human before it runs.",
		map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to run",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Optional subdirectory to run in, relative to the workspace",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Command timeout in seconds; defaults to 120, maximum 900",
			},
		},
		[]string{"command"},
	)
}

func (m *FileManager) BashExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	ctx, cancel := context.WithTimeoutCause(
		ctx,
		defaultBashTimeout,
		fmt.Errorf("bash execution timeout"),
	)
	defer cancel() // Always defer cancel to clean up resources!

	args, err := contracts.DecodeToolArguments[bashArguments](contracts.BashTool, tc)
	if err != nil {
		return "", err
	}
	if err := bashToolValidate(args); err != nil {
		return "", wrapToolError(contracts.BashTool, err, "invalid_arguments", "bash arguments are invalid")
	}

	result, err := m.Bash(ctx, args.Command, args.Workdir, args.Timeout)
	if err != nil {
		return "", wrapToolError(contracts.BashTool, err, "bash_failed", "the command could not be run")
	}
	return assemble(result)
}

func bashToolValidate(args bashArguments) error {
	if args.Command == "" {
		return contracts.NewToolExecutionError(contracts.BashTool, "invalid_arguments", "bash requires a command", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(args.Command, contracts.ResponsibilityAgent); err != nil {
		return err
	}
	if strings.ContainsRune(args.Command, '\r') {
		return contracts.NewToolExecutionError(contracts.BashTool, "invisible_character", "commands may not contain carriage returns", contracts.ResponsibilityAgent, nil)
	}
	if len(args.Command) > maxCommandBytes {
		return contracts.NewToolExecutionError(contracts.BashTool, "command_too_large", "the command exceeds the size limit", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(args.Workdir, contracts.ResponsibilityAgent); err != nil {
		return err
	}
	if args.Timeout != nil {
		timeout := time.Duration(*args.Timeout) * time.Second
		if timeout < time.Second {
			return contracts.NewToolExecutionError(contracts.BashTool, "invalid_arguments", "timeout must be at least 1 second", contracts.ResponsibilityAgent, nil)
		}
		if timeout > maxBashTimeout {
			return contracts.NewToolExecutionError(contracts.BashTool, "invalid_arguments", "timeout exceeds the maximum of 900 seconds", contracts.ResponsibilityAgent, nil)
		}
	}
	return checkDangerousCommand(args.Command)
}

func (m *FileManager) Bash(ctx context.Context, command, workdir string, timeout *int) (BashResult, error) {
	dir := m.root
	if workdir != "" {
		resolved, err := m.resolve(workdir, true)
		if err != nil {
			return BashResult{}, err
		}
		dir = resolved
	}

	deadline := defaultBashTimeout
	if timeout != nil {
		deadline = time.Duration(*timeout) * time.Second
	}

	runCtx, cancel := context.WithTimeoutCause(ctx, deadline, fmt.Errorf("bash exceeded the %s execution limit", deadline))
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	prepareCommand(cmd)

	go func() {
		<-runCtx.Done()
		killProcessGroup(cmd)
	}()

	output, err := cmd.CombinedOutput()
	result := BashResult{Output: string(output)}
	if len(result.Output) > maxToolContentBytes {
		result.Output = result.Output[:maxToolContentBytes]
		result.Truncated = true
	}
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		return result, filesystemErr("command_timed_out", "the command exceeded the runtime limit", contracts.ResponsibilityTool, nil)
	}
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, filesystemErr("command_failed", "the command could not be started", contracts.ResponsibilityEnvironment, err)
	}
	return result, nil
}
