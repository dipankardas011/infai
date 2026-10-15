package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testClipboard(goos string, env map[string]string, tools map[string]string, output func(string, []string) ([]byte, error)) *systemClipboard {
	return &systemClipboard{
		goos: goos,
		getenv: func(key string) string {
			return env[key]
		},
		lookPath: func(name string) (string, error) {
			if path, ok := tools[name]; ok {
				return path, nil
			}
			return "", errors.New("not found")
		},
		output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return output(name, args)
		},
	}
}

func TestSystemClipboardUnavailable(t *testing.T) {
	c := testClipboard("linux", nil, nil, func(string, []string) ([]byte, error) { return nil, nil })
	if _, err := c.ReadImage(context.Background()); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("err=%v want ErrClipboardUnavailable", err)
	}
}

func TestSystemClipboardEmpty(t *testing.T) {
	c := testClipboard("linux", nil, map[string]string{"xclip": "/usr/bin/xclip"}, func(string, []string) ([]byte, error) {
		return nil, nil
	})
	if _, err := c.ReadImage(context.Background()); !errors.Is(err, ErrClipboardEmpty) {
		t.Fatalf("err=%v want ErrClipboardEmpty", err)
	}
}

func TestSystemClipboardReadsImageBytes(t *testing.T) {
	want := testPNG(t, 3, 3)
	c := testClipboard("linux", nil, map[string]string{"xclip": "/usr/bin/xclip"}, func(string, []string) ([]byte, error) {
		return want, nil
	})
	got, err := c.ReadImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("clipboard bytes changed")
	}
}

func TestSystemClipboardCommandSelection(t *testing.T) {
	wayland := testClipboard("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, nil, nil)
	if commands := wayland.commands(); len(commands) == 0 || commands[0].name != "wl-paste" {
		t.Fatalf("wayland commands=%+v", commands)
	}

	x11 := testClipboard("linux", nil, nil, nil)
	if commands := x11.commands(); len(commands) == 0 || commands[0].name != "xclip" {
		t.Fatalf("x11 commands=%+v", commands)
	}

	mac := testClipboard("darwin", nil, nil, nil)
	if commands := mac.commands(); len(commands) == 0 || commands[0].name != "pngpaste" {
		t.Fatalf("darwin commands=%+v", commands)
	}

	windows := testClipboard("windows", nil, nil, nil)
	if commands := windows.commands(); len(commands) != 0 {
		t.Fatalf("windows commands=%+v", commands)
	}
}

func TestLimitedWriterRejectsOversize(t *testing.T) {
	w := &limitedWriter{limit: 4}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := w.Write([]byte("def")); !errors.Is(err, ErrClipboardTooLarge) {
		t.Fatalf("err=%v want ErrClipboardTooLarge", err)
	}
}

func TestSystemClipboardReportsTooLarge(t *testing.T) {
	c := testClipboard("linux", nil, map[string]string{"xclip": "/usr/bin/xclip"}, func(string, []string) ([]byte, error) {
		return nil, ErrClipboardTooLarge
	})
	if _, err := c.ReadImage(context.Background()); !errors.Is(err, ErrClipboardTooLarge) {
		t.Fatalf("err=%v want ErrClipboardTooLarge", err)
	}
}

func TestSystemClipboardFallsBackToJPEG(t *testing.T) {
	var tried []string
	c := testClipboard("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, map[string]string{"wl-paste": "/usr/bin/wl-paste"}, func(_ string, args []string) ([]byte, error) {
		tried = append(tried, args[len(args)-1])
		if args[len(args)-1] == "image/jpeg" {
			return []byte("jpeg"), nil
		}
		return nil, errors.New("no png")
	})
	got, err := c.ReadImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "jpeg" {
		t.Fatalf("got=%q want jpeg", got)
	}
	if len(tried) != 2 {
		t.Fatalf("tried=%v want png then jpeg", tried)
	}
}

// TestClipboardWriteHelper is not a test: it is the helper the two write tests
// run, re-executed from the test binary so a real child process is what gets
// exercised.
func TestClipboardWriteHelper(t *testing.T) {
	mode := os.Getenv("INFAI_CLIPBOARD_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "owner":
		// Takes the text and then keeps serving it, the way wl-copy does. The
		// sleep only has to outlive the assertion; it bounds how long the helper
		// can outlive the test.
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(15 * time.Second)
	case "wedged":
		// Present, but never reads.
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestClipboardWriteReturnsOnceAnOwnerHelperTakesTheText(t *testing.T) {
	t.Setenv("INFAI_CLIPBOARD_HELPER", "owner")

	start := time.Now()
	if err := runClipboardWrite(context.Background(), os.Args[0], []string{"-test.run=^TestClipboardWriteHelper$"}, "hello", 30*time.Second); err != nil {
		t.Fatalf("write to a helper that owns the clipboard = %v", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the write waited %s for a helper that had taken the text", waited)
	}
}

func TestClipboardWriteGivesUpOnAHelperThatNeverReads(t *testing.T) {
	t.Setenv("INFAI_CLIPBOARD_HELPER", "wedged")

	// Larger than a pipe buffer, so the write cannot complete behind the
	// helper's back.
	text := strings.Repeat("x", 1<<20)
	start := time.Now()
	if err := runClipboardWrite(context.Background(), os.Args[0], []string{"-test.run=^TestClipboardWriteHelper$"}, text, 200*time.Millisecond); err == nil {
		t.Fatal("a helper that never read was reported as having taken the text")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("gave up after %s", waited)
	}
}
