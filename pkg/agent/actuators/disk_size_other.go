//go:build !linux

package actuators

import "os"

func diskSizeBytes(info os.FileInfo) int64 {
	return info.Size()
}
