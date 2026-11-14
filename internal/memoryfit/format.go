package memoryfit

import "fmt"

// FormatBytes renders a memory size for user-facing messages.
func FormatBytes(bytes uint64) string {
	const gib = 1024 * 1024 * 1024
	if bytes >= gib {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
	}
	return fmt.Sprintf("%.0f MiB", float64(bytes)/(1024*1024))
}
