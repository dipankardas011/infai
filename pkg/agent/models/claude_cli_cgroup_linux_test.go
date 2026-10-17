//go:build linux

package models

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestCgroupV2PathParsing(t *testing.T) {
	cases := []struct {
		name, contents, want string
		ok                   bool
	}{
		{"v2 line", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/infaiw.service/supervisor", "/user.slice/user-1000.slice/user@1000.service/app.slice/infaiw.service/supervisor", true},
		{"blank lines around it", "\n0::/a/b\n", "/a/b", true},
		{"v1 only", "12:memory:/a\n1:name=systemd:/b\n", "", false},
		{"empty path", "0::\n", "", false},
		{"no v2", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := cgroupV2Path(tc.contents)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("cgroupV2Path(%q) = %q, %v; want %q, %v", tc.contents, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The provider caps a turn only when the unit delegated a cgroup subtree. This
// asserts the limits are actually written; on a host that has not delegated it
// skips rather than failing, because running unconstrained is the documented
// fallback.
func TestClaudeCgroupCarriesTheLimitsWhenDelegated(t *testing.T) {
	cgroup, err := newClaudeCgroup(claudeCgroupLimits{CPUMax: "50000 100000", MemoryMax: 64 << 20, PidsMax: 64})
	if err != nil {
		t.Skipf("cgroup subtree is not delegated to this process: %v", err)
	}
	defer cgroup.remove()

	for _, tc := range []struct{ file, want string }{
		{"cpu.max", "50000 100000"},
		{"memory.max", strconv.FormatUint(64<<20, 10)},
		{"memory.swap.max", "0"},
		{"pids.max", "64"},
	} {
		raw, err := os.ReadFile(filepath.Join(cgroup.path, tc.file))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if got := strings.TrimSpace(string(raw)); got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// The child must be born inside the cgroup, not moved into it afterwards.
func TestClaudeCgroupPlacesTheChildAtFork(t *testing.T) {
	cgroup, err := newClaudeCgroup(claudeCgroupLimits{MemoryMax: 256 << 20, PidsMax: 64})
	if err != nil {
		t.Skipf("cgroup subtree is not delegated to this process: %v", err)
	}
	defer cgroup.remove()

	cmd := exec.Command("/bin/sh", "-c", "cat /proc/self/cgroup")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	release, err := cgroup.attach(cmd)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	release()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// /proc/self/cgroup reports the path relative to the cgroup mount.
	reported := strings.TrimPrefix(cgroup.path, "/sys/fs/cgroup")
	if got := strings.TrimSpace(string(out)); !strings.Contains(got, reported) {
		t.Fatalf("the child ran in %q, which is not %q", got, reported)
	}
}
