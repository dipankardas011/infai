package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/vision"
)

// Clipboard image failures surfaced to the user. They are distinguishable so
// the TUI can explain whether the problem is missing tooling or an empty
// clipboard.
var (
	ErrClipboardUnavailable = errors.New("clipboard: no image clipboard tool is available")
	ErrClipboardEmpty       = errors.New("clipboard: no image found on the clipboard")
	ErrClipboardTooLarge    = errors.New("clipboard: image exceeds the size limit")
)

// clipboardTimeout bounds a single clipboard read so a hung helper cannot leak
// a process for the life of the app.
const clipboardTimeout = 10 * time.Second

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
		output:   runClipboardCommand,
	}
}

// runClipboardCommand runs one clipboard helper with a hard timeout and a
// bounded stdout buffer, so an oversized image cannot be fully materialized in
// memory and a hung helper is killed.
func runClipboardCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	writer := &limitedWriter{limit: vision.MaxImageBytes + 1}
	cmd.Stdout = writer
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(err, ErrClipboardTooLarge) {
			return nil, ErrClipboardTooLarge
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return writer.buf.Bytes(), nil
}

// limitedWriter stops accepting bytes past its limit and reports
// ErrClipboardTooLarge, which makes os/exec tear the child process down.
type limitedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		return 0, ErrClipboardTooLarge
	}
	return w.buf.Write(p)
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
	tooLarge := false
	for _, command := range commands {
		if _, err := c.lookPath(command.name); err != nil {
			continue
		}
		foundTool = true
		data, err := c.output(ctx, command.name, command.args...)
		if err != nil {
			if errors.Is(err, ErrClipboardTooLarge) {
				tooLarge = true
			}
			continue
		}
		if len(data) == 0 {
			continue
		}
		return data, nil
	}
	if tooLarge {
		return nil, ErrClipboardTooLarge
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
