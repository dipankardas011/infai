package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Clipboard image failures surfaced to the user. They are distinguishable so
// the TUI can explain whether the problem is missing tooling or an empty
// clipboard.
var (
	ErrClipboardUnavailable = errors.New("clipboard: no image clipboard tool is available")
	ErrClipboardEmpty       = errors.New("clipboard: no image found on the clipboard")
)

// ClipboardReader reads one image from the system clipboard. It is an
// interface so platform support (Windows, OSC 5522) can be added without
// touching the TUI, and so tests can inject a reader.
type ClipboardReader interface {
	ReadImage(ctx context.Context) ([]byte, error)
}

type clipboardCommand struct {
	name string
	args []string
}

// systemClipboard selects a platform clipboard tool. All process execution is
// deferred to the injected output func so callers can drive it from a tea.Cmd
// without blocking the Bubble Tea update loop.
type systemClipboard struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	output   func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func defaultClipboard() *systemClipboard {
	return &systemClipboard{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

// ReadImage tries each clipboard command for the current platform. A missing
// tool reports ErrClipboardUnavailable; a present tool that returns no bytes
// reports ErrClipboardEmpty.
func (c *systemClipboard) ReadImage(ctx context.Context) ([]byte, error) {
	commands := c.commands()
	if len(commands) == 0 {
		return nil, ErrClipboardUnavailable
	}
	foundTool := false
	for _, command := range commands {
		if _, err := c.lookPath(command.name); err != nil {
			continue
		}
		foundTool = true
		data, err := c.output(ctx, command.name, command.args...)
		if err != nil {
			continue
		}
		if len(data) == 0 {
			continue
		}
		return data, nil
	}
	if !foundTool {
		return nil, ErrClipboardUnavailable
	}
	return nil, ErrClipboardEmpty
}

func (c *systemClipboard) commands() []clipboardCommand {
	switch c.goos {
	case "darwin":
		return []clipboardCommand{{name: "pngpaste", args: []string{"-"}}}
	case "windows":
		return nil
	default:
		if isWayland(c.getenv) {
			return []clipboardCommand{
				{name: "wl-paste", args: []string{"--type", "image/png"}},
				{name: "wl-paste", args: []string{"--type", "image/jpeg"}},
			}
		}
		return []clipboardCommand{
			{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
			{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/jpeg", "-o"}},
		}
	}
}

func isWayland(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	if getenv("WAYLAND_DISPLAY") != "" {
		return true
	}
	return getenv("XDG_SESSION_TYPE") == "wayland"
}

// clipboardImageName builds a stable display name for a pasted image once its
// media type has been detected from the bytes.
func clipboardImageName(number int, mediaType string) string {
	extension := "png"
	if mediaType == "image/jpeg" {
		extension = "jpg"
	}
	return fmt.Sprintf("clipboard-%d.%s", number, extension)
}
