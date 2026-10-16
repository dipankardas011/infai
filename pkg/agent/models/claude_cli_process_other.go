//go:build !unix

package models

import (
	"os"
	"os/exec"
)

func configureClaudeProcess(cmd *exec.Cmd) {}

func terminateClaudeProcess(process *os.Process) {
	if process != nil {
		_ = process.Kill()
	}
}
