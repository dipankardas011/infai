//go:build unix

package models

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureClaudeProcess puts the CLI in its own process group so cancelling a
// turn can take down anything it started.
func configureClaudeProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateClaudeProcess(process *os.Process) {
	if process == nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGTERM)
	time.Sleep(250 * time.Millisecond)
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
}
