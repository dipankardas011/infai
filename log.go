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
	attrs []slog.Attr
	group string
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

	for _, a := range h.attrs {
		fmt.Fprintf(h.w, " %s%s=", h.group, a.Key)
		levelColor(r.Level).Fprintf(h.w, "%v", a.Value)
	}

	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(h.w, " %s%s=", h.group, a.Key)
		levelColor(r.Level).Fprintf(h.w, "%v", a.Value)
		return true
	})

	fmt.Fprintln(h.w)
	return nil
}

func (h *coloredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &coloredHandler{w: h.w, level: h.level, attrs: newAttrs, group: h.group}
}

func (h *coloredHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	group := h.group + name + "."
	return &coloredHandler{w: h.w, level: h.level, attrs: h.attrs, group: group}
}

func init() {
	slog.SetDefault(slog.New(newColoredHandler(os.Stderr, slog.LevelDebug)))
}
