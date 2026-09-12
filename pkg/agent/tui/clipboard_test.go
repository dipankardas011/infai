package tui

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

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
