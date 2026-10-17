//go:build !linux

package models

import (
	"errors"
	"os/exec"
)

// cgroup v2 with CLONE_INTO_CGROUP is a Linux facility; elsewhere the provider
// runs unconstrained.
type claudeCgroup struct{}

type claudeCgroupLimits struct {
	MemoryMax uint64
	PidsMax   int
}

var errCgroupsUnsupported = errors.New("cgroups are not supported on this platform")

func newClaudeCgroup(claudeCgroupLimits) (*claudeCgroup, error) {
	return nil, errCgroupsUnsupported
}

func (c *claudeCgroup) attach(*exec.Cmd) (func(), error) { return func() {}, nil }

func (c *claudeCgroup) remove() {}

func (c *claudeCgroup) oomKills() (uint64, bool) { return 0, false }
