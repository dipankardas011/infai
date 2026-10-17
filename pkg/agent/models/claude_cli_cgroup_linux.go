//go:build linux

package models

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// A per-call cgroup bounds the CLI and everything it starts. It is only
// available when the unit that runs infai delegates its cgroup subtree to the
// process: Delegate=yes plus DelegateSubgroup, so the unit's own cgroup is left
// empty and controllers can be switched on for its children. Without that
// delegation the provider runs unconstrained, exactly as before.
type claudeCgroup struct {
	path string
}

type claudeCgroupLimits struct {
	MemoryMax uint64
	PidsMax   int
}

var claudeCgroupSeq atomic.Uint64

// newClaudeCgroup creates a cgroup for one CLI turn, or returns a nil cgroup
// with an error explaining that the subtree is not delegated.
func newClaudeCgroup(limits claudeCgroupLimits) (*claudeCgroup, error) {
	unit, err := delegatedUnitCgroup()
	if err != nil {
		return nil, err
	}
	// One cgroup per call: several sessions share this process, and a turn
	// must not inherit another turn's accounting.
	name := fmt.Sprintf("claude-cli-%d-%d", os.Getpid(), claudeCgroupSeq.Add(1))
	path := filepath.Join(unit, name)
	_ = os.Remove(path) // a crashed earlier run may have left it behind
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, fmt.Errorf("claude cli: create cgroup: %w", err)
	}
	cgroup := &claudeCgroup{path: path}
	if err := cgroup.writeLimits(limits); err != nil {
		cgroup.remove()
		return nil, err
	}
	return cgroup, nil
}

func (c *claudeCgroup) writeLimits(limits claudeCgroupLimits) error {
	// Swap is switched off so a runaway process cannot exceed the cap by
	// paging, which is the point of the cap.
	if err := os.WriteFile(filepath.Join(c.path, "memory.swap.max"), []byte("0"), 0); err != nil {
		return fmt.Errorf("claude cli: cgroup swap limit: %w", err)
	}
	if limits.MemoryMax > 0 {
		if err := os.WriteFile(filepath.Join(c.path, "memory.max"), []byte(strconv.FormatUint(limits.MemoryMax, 10)), 0); err != nil {
			return fmt.Errorf("claude cli: cgroup memory limit: %w", err)
		}
	}
	if limits.PidsMax > 0 {
		if err := os.WriteFile(filepath.Join(c.path, "pids.max"), []byte(strconv.Itoa(limits.PidsMax)), 0); err != nil {
			return fmt.Errorf("claude cli: cgroup task limit: %w", err)
		}
	}
	return nil
}

// attach places the command in the cgroup at fork time with CLONE_INTO_CGROUP,
// so the process never runs outside its limit. The returned release closes the
// directory handle and must be called once the command has started.
func (c *claudeCgroup) attach(cmd *exec.Cmd) (func(), error) {
	dir, err := os.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("claude cli: open cgroup: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(dir.Fd())
	return func() { _ = dir.Close() }, nil
}

func (c *claudeCgroup) remove() { _ = os.Remove(c.path) }

// oomKills reports how many processes in this cgroup were killed for exceeding
// its memory limit. Read before remove, while the cgroup still exists.
func (c *claudeCgroup) oomKills() (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(c.path, "memory.events"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), "oom_kill ")
		if !found {
			continue
		}
		kills, err := strconv.ParseUint(value, 10, 64)
		return kills, err == nil
	}
	return 0, false
}

// delegatedUnitCgroup finds the cgroup the unit delegated to this process.
func delegatedUnitCgroup() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("claude cli: read own cgroup: %w", err)
	}
	relative, ok := cgroupV2Path(string(raw))
	if !ok {
		return "", errors.New("cgroup v2 is not in use")
	}
	self := filepath.Join("/sys/fs/cgroup", relative)
	// With DelegateSubgroup we run in <unit>/<subgroup>, and the unit is the
	// parent. Try that first, then the cgroup we are in.
	//
	// Only a unit may be used. Walking further up would reach the slice, which
	// is systemd's own and already has controllers enabled; creating cgroups
	// there works by accident, puts them outside the unit, and leaves systemd
	// unaware of them.
	for _, candidate := range []string{filepath.Dir(self), self} {
		if !isSystemdUnit(candidate) {
			continue
		}
		if err := enableCgroupControllers(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("the unit's cgroup subtree is not delegated")
}

func isSystemdUnit(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".service") || strings.HasSuffix(base, ".scope")
}

// cgroupV2Path extracts the path from /proc/self/cgroup's v2 line ("0::/path").
func cgroupV2Path(contents string) (string, bool) {
	for _, line := range strings.Split(contents, "\n") {
		path, found := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if found && path != "" {
			return path, true
		}
	}
	return "", false
}

// enableCgroupControllers switches on the controllers the provider needs,
// leaving any already enabled. A cgroup that holds processes refuses this,
// which is how a non-delegated unit is detected.
func enableCgroupControllers(unit string) error {
	control := filepath.Join(unit, "cgroup.subtree_control")
	current, err := os.ReadFile(control)
	if err != nil {
		return err
	}
	var missing strings.Builder
	for _, controller := range []string{"memory", "pids"} {
		if !strings.Contains(string(current), controller) {
			missing.WriteString("+" + controller + " ")
		}
	}
	if missing.Len() == 0 {
		return nil
	}
	return os.WriteFile(control, []byte(strings.TrimSpace(missing.String())), 0)
}
