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
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/vision"
)

// Clipboard image failures surfaced to the user, distinguishable so the TUI can
// tell missing tooling apart from an empty or oversized clipboard.
var (
	ErrClipboardUnavailable = errors.New("clipboard: no image clipboard tool is available")
	ErrClipboardEmpty       = errors.New("clipboard: no image found on the clipboard")
	ErrClipboardTooLarge    = errors.New("clipboard: image exceeds the size limit")
)

// clipboardTimeout bounds one exchange with a helper: a read, and the handover
// half of a write.
const clipboardTimeout = 10 * time.Second

// Clipboard is the system clipboard as the harness uses it: one image in, one
// selection of text out. It is an interface so tests can inject one.
type Clipboard interface {
	ReadImage(ctx context.Context) ([]byte, error)
	WriteText(ctx context.Context, text string) (string, error)
}

type clipboardCommand struct {
	name string
	args []string
}

// systemClipboard selects a platform clipboard tool. Process execution goes
// through the injected funcs so a caller can drive it from a tea.Cmd without
// blocking the Bubble Tea update loop.
type systemClipboard struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	output   func(ctx context.Context, name string, args ...string) ([]byte, error)
	write    func(ctx context.Context, path string, args []string, text string, timeout time.Duration) error
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

// runClipboardCommand reads one helper with a hard timeout and a bounded buffer,
// so an oversized image is never fully materialized and a hung helper is killed.
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

// runClipboardWrite hands text to a helper, bounded by timeout. Success is the
// helper taking every byte, not the process ending: a clipboard owner forks and
// then serves pastes for as long as it holds the selection. Its stderr stays on
// the null device rather than a pipe, because the forked owner inherits that pipe
// and keeps it open, which hangs anything waiting on the handover.
func runClipboardWrite(ctx context.Context, path string, args []string, text string, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() {
		// io.Copy rather than one write: a pipe may accept only part of the
		// selection, and the helper must see all of it before it owns the text.
		_, err := io.Copy(stdin, strings.NewReader(text))
		if cerr := stdin.Close(); err == nil {
			err = cerr
		}
		written <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-written:
		if err != nil {
			killHelper(cmd)
			return err
		}
	case <-timer.C:
		killHelper(cmd)
		return fmt.Errorf("%s did not take the text within %s", path, timeout)
	}
	// The helper owns the clipboard now, so it is reaped in the background
	// rather than waited on.
	go func() { _ = cmd.Wait() }()
	return nil
}

// killHelper stops a helper that never took the text and reaps it, so a failed
// copy leaves no process behind.
func killHelper(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// WriteText puts text on the system clipboard and names the helper that took it.
// No usable helper reports ErrClipboardUnavailable, which the TUI turns into an
// OSC 52 write.
func (c *systemClipboard) WriteText(ctx context.Context, text string) (string, error) {
	if text == "" {
		return "", ErrClipboardEmpty
	}
	for _, command := range c.writeCommands() {
		path, err := c.lookPath(command.name)
		if err != nil {
			continue
		}
		if err := c.write(ctx, path, command.args, text, clipboardTimeout); err != nil {
			continue
		}
		return command.name, nil
	}
	return "", ErrClipboardUnavailable
}

// limitedWriter refuses bytes past its limit with ErrClipboardTooLarge, which
// tears the helper down before it can stream a whole oversized image.
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

// ReadImage tries each clipboard command for the platform. A missing tool
// reports ErrClipboardUnavailable; a present tool returning no bytes reports
// ErrClipboardEmpty.
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

// writeCommands selects the helper that takes text. A write carries no media
// type: the platform's tool stores whatever arrives on stdin.
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
