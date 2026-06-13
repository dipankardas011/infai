package tui

import (
	"bytes"
	"os/exec"
)

// CopyToClipboard copies text to the system clipboard.
// Returns name of the command used, or empty string + nil if unavailable.
func CopyToClipboard(text string) (string, error) {
	for _, entry := range []struct {
		bin  string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"pbcopy", nil},
	} {
		path, err := exec.LookPath(entry.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, entry.args...)
		cmd.Stdin = bytes.NewBufferString(text)
		if err := cmd.Run(); err != nil {
			return entry.bin, err
		}
		return entry.bin, nil
	}
	return "", nil
}
