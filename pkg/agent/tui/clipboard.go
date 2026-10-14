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

// Clipboard is the system clipboard as the harness uses it: one image in, and
// one selection of text out. It is an interface so platform support (Windows,
// OSC 5522) can be added without touching the TUI, and so tests can inject one.
type Clipboard interface {
	ReadImage(ctx context.Context) ([]byte, error)
	WriteText(ctx context.Context, text string) (string, error)
}

type clipboardCommand struct {
	name string
	args []string
}

// systemClipboard selects a platform clipboard tool. All process execution is
// deferred to the injected output and write funcs so callers can drive it from
// a tea.Cmd without blocking the Bubble Tea update loop.
type systemClipboard struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	output   func(ctx context.Context, name string, args ...string) ([]byte, error)
	write    func(ctx context.Context, path string, args []string, text string) error
}

func defaultClipboard() *systemClipboard {
	return &systemClipboard{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		output:   runClipboardCommand,
		write:    runClipboardWrite,
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

// runClipboardWrite feeds all text to the platform clipboard helper and returns
// once the helper has accepted it. Some helpers stay alive to own the clipboard;
// their lifetime is bounded by the caller's context and reaped in the background.
func runClipboardWrite(ctx context.Context, path string, args []string, text string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, text); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// WriteText puts text on the system clipboard and names the helper that took it.
// A machine with no helper reports ErrClipboardUnavailable, which the TUI turns
// into an OSC 52 write.
func (c *systemClipboard) WriteText(ctx context.Context, text string) (string, error) {
	if text == "" {
		return "", ErrClipboardEmpty
	}
	for _, command := range c.writeCommands() {
		path, err := c.lookPath(command.name)
		if err != nil {
			continue
		}
		if err := c.write(ctx, path, command.args, text); err != nil {
			continue
		}
		return command.name, nil
	}
	return "", ErrClipboardUnavailable
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

// writeCommands selects the helper that takes text for the clipboard. Unlike a
// read, a write needs no media type: the platform's tool takes whatever arrives
// on stdin.
func (c *systemClipboard) writeCommands() []clipboardCommand {
	switch c.goos {
	case "darwin":
		return []clipboardCommand{{name: "pbcopy"}}
	case "windows":
		return []clipboardCommand{{name: "clip"}}
	default:
		if isWayland(c.getenv) {
			return []clipboardCommand{{name: "wl-copy"}}
		}
		return []clipboardCommand{{name: "xclip", args: []string{"-selection", "clipboard"}}}
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
