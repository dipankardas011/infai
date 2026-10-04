//go:build unix

package actuators

import (
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the command and every process in its group. bash starts
// its children in the same group (Setpgid above), so signalling the command
// alone would leave them running.
//
// It is called through exec's own cancellation, never from a watcher of ours:
// reading cmd.Process before Start has finished assigning it is a data race.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
