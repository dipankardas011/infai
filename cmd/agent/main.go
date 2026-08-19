package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/engine"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	cfg, err := config.LoadConfig()
	if err != nil {
		panic(err)
	}

	wlog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level: func() slog.Level {
			switch cfg.Logging.Level {
			case "debug":
				return slog.LevelDebug
			case "info":
				return slog.LevelInfo
			case "warn":
				return slog.LevelWarn
			case "error":
				return slog.LevelError
			default:
				return slog.LevelDebug
			}
		}(),
	}))
	wlog.DebugContext(ctx, "Loaded config", "config", cfg)

	svc, err := engine.NewInfaiAgentEngine(wlog, cfg)
	if err != nil {
		wlog.ErrorContext(ctx, "Error creating engine", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := svc.Run(ctx); err != nil {
			wlog.ErrorContext(ctx, "Error from engine", "error", err)
		}
		wlog.InfoContext(ctx, "Engine work done")
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	sig := <-sigChan
	wlog.DebugContext(ctx, "Received terminate signal", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := svc.Shutdown(shutdownCtx); err != nil {
		wlog.ErrorContext(shutdownCtx, "Error during engine shutdown", "error", err)
	} else {
		wlog.InfoContext(shutdownCtx, "engine shutdown completed gracefully")
	}

	wlog.DebugContext(ctx, "Shutdown complete")
}
