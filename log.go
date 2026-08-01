package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/fatih/color"
)

type coloredHandler struct {
	w     io.Writer
	level slog.Leveler
}

func newColoredHandler(w io.Writer, level slog.Leveler) *coloredHandler {
	return &coloredHandler{w: w, level: level}
}

func (h *coloredHandler) Enabled(_ context.Context, lv slog.Level) bool {
	return lv >= h.level.Level()
}

func levelColor(lv slog.Level) *color.Color {
	switch lv {
	case slog.LevelError:
		return color.New(color.FgRed)
	case slog.LevelWarn:
		return color.New(color.FgYellow)
	case slog.LevelInfo:
		return color.New(color.FgCyan)
	default:
		return color.New(color.FgWhite)
	}
}

func (h *coloredHandler) Handle(_ context.Context, r slog.Record) error {
	t := r.Time.Format(time.TimeOnly)
	fmt.Fprintf(h.w, "%s ", t)
	levelColor(r.Level).Fprintf(h.w, "%-5s", r.Level)
	fmt.Fprintf(h.w, " %s", r.Message)

	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(h.w, " %s=", a.Key)
		levelColor(r.Level).Fprintf(h.w, "%v", a.Value)
		return true
	})

	fmt.Fprintln(h.w)
	return nil
}

func (h *coloredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *coloredHandler) WithGroup(name string) slog.Handler {
	return h
}

func init() {
	slog.SetDefault(slog.New(newColoredHandler(os.Stderr, slog.LevelDebug)))
}
