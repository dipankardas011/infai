//go:build linux

package actuators

import (
	"os"
	"syscall"
)

func diskSizeBytes(info os.FileInfo) int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Blocks <= 0 {
		return info.Size()
	}
	return stat.Blocks * 512
}
