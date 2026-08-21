package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/engine"
	"github.com/dipankardas011/infai/pkg/agent/server"
	"github.com/dipankardas011/infai/pkg/agent/tui"
	"github.com/spf13/cobra"
)

var (
	host string
	port int
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "infai agent engine",
		RunE:  runTUI,
	}
	cmd.Flags().StringVar(&host, "host", "localhost", "agent server host to attach to")
	cmd.Flags().IntVar(&port, "port", 6000, "agent server port to attach to")
	cmd.AddCommand(newServerCmd())
	return cmd
}

// server is configured purely through INFAI_AGENT_* environment variables;
// it accepts no flags.
func newServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "run the agent engine HTTP server (env-only configuration)",
		RunE:  runServer,
	}
}

func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level: func() slog.Level {
			switch level {
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
}

// runTUI is the default mode: a line-based chat client that attaches to a
// running <binary> server.
func runTUI(cmd *cobra.Command, args []string) error {
	client := tui.NewRemoteClient(fmt.Sprintf("http://%s:%d", host, port))
	return tui.Run(context.Background(), client, os.Stdin, os.Stdout)
}

// runServer exposes the engine over HTTP so any client (web UI, remote TUI,
// curl) can create and reuse sessions.
func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	wlog := newLogger(cfg.Logging.Level)
	wlog.DebugContext(ctx, "Loaded config", "config", cfg)

	eng, err := engine.NewInfaiAgentEngine(wlog, cfg)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := server.New(wlog, eng, addr, cfg.EnableHealthz)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			wlog.ErrorContext(ctx, "server error", "error", err)
			cancel()
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	sig := <-sigChan
	wlog.DebugContext(ctx, "Received terminate signal", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		wlog.ErrorContext(shutdownCtx, "server shutdown error", "error", err)
	}
	if err := eng.Shutdown(shutdownCtx); err != nil {
		wlog.ErrorContext(shutdownCtx, "engine shutdown error", "error", err)
	}
	wlog.InfoContext(ctx, "shutdown complete")
	return nil
}
