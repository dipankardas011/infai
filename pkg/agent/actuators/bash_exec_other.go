//go:build !unix

package actuators

import "os/exec"

func prepareCommand(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) error { return nil }
