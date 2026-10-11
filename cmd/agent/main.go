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

	internalconfig "github.com/dipankardas011/infai/internal/config"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/engine"
	"github.com/dipankardas011/infai/pkg/agent/server"
	"github.com/dipankardas011/infai/pkg/agent/tui"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	host      string
	port      int
	sessionID string
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		// Cobra's default on a RunE error is to print the error and dump the
		// usage/help block. Runtime failures are already surfaced (REPL Notice
		// or the error text itself) — the help menu adds noise, so silence it
		// and print the error once.
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "infaiw",
		Short:         "infai workflow and agent engine",
		Version:       internalconfig.Version(),
		RunE:          runTUI,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&host, "host", "localhost", "agent server host to attach to")
	cmd.Flags().IntVar(&port, "port", 6000, "agent server port to attach to")
	cmd.Flags().StringVar(&sessionID, "session", "", "resume a saved session by uuid")
	cmd.AddCommand(newServerCmd())
	cmd.AddCommand(newProviderCmd())
	cmd.AddCommand(newMCPCmd())
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

func newProviderCmd() *cobra.Command {
	provider := &cobra.Command{
		Use:     "provider",
		Aliases: []string{"providers"},
		Short:   "manage model provider credentials",
		Args:    cobra.NoArgs,
	}
	addServerFlags(provider)
	provider.AddCommand(
		&cobra.Command{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "list configured provider models",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunProviderList(cmd.Context(), remoteClient(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "login",
			Short: "log in to a managed provider",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunProviderLogin(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "logout",
			Short: "log out from a managed provider",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunProviderLogout(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout())
			},
		},
	)
	return provider
}

func remoteClient() *tui.RemoteClient {
	return tui.NewRemoteClient(fmt.Sprintf("http://%s:%d", host, port))
}

// addServerFlags lets a command that talks to the agent server choose which one.
// Only the client commands get these: `infaiw server` is configured through the
// INFAI_AGENT_* environment and accepts no flags at all.
func addServerFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&host, "host", "localhost", "agent server host to attach to")
	cmd.PersistentFlags().IntVar(&port, "port", 6000, "agent server port to attach to")
}

func newMCPCmd() *cobra.Command {
	mcpCmd := &cobra.Command{
		Use:     "mcp",
		Aliases: []string{"mcps"},
		Short:   "manage MCP servers and their authorizations",
		Args:    cobra.NoArgs,
	}
	addServerFlags(mcpCmd)
	mcpCmd.AddCommand(
		&cobra.Command{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "list configured MCP servers and their token state",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunMCPList(cmd.Context(), remoteClient(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "add",
			Short: "add an MCP server, authorizing it when it uses OAuth",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunMCPAdd(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "edit [name]",
			Short: "change an MCP server's settings",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tui.RunMCPEdit(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout(), optionalName(args))
			},
		},
		&cobra.Command{
			Use:     "remove [name]",
			Aliases: []string{"rm"},
			Short:   "remove an MCP server and its stored authorization",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tui.RunMCPRemove(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout(), optionalName(args))
			},
		},
		&cobra.Command{
			Use:     "refresh [name]",
			Aliases: []string{"login"},
			Short:   "authorize an MCP server again",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tui.RunMCPRefresh(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout(), optionalName(args))
			},
		},
	)
	return mcpCmd
}

// optionalName returns the single optional argument, or "" when it was omitted,
// which leaves the command asking for it interactively.
func optionalName(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
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

func runTUI(cmd *cobra.Command, args []string) error {
	client := tui.NewRemoteClient(fmt.Sprintf("http://%s:%d", host, port))

	var resume uuid.UUID
	if sessionID != "" {
		id, err := uuid.Parse(sessionID)
		if err != nil {
			return fmt.Errorf("invalid --session uuid: %w", err)
		}
		resume = id
	}

	return tui.Run(context.Background(), client, os.Stdin, os.Stdout, tui.RunOptions{
		SessionID: resume,
	})
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

	eng, err := engine.NewInfaiAgentEngine(ctx, wlog, cfg)
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

	if err := eng.Shutdown(shutdownCtx); err != nil {
		wlog.ErrorContext(shutdownCtx, "engine shutdown error", "error", err)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		wlog.ErrorContext(shutdownCtx, "server shutdown error", "error", err)
	}
	wlog.InfoContext(ctx, "shutdown complete")
	return nil
}
