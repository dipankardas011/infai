package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/browserext"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// syncBrowserExtension refreshes the extension in its fixed folder on every
// start, so the installed copy always matches this build and carries the
// current daemon address and pairing token. Chrome keeps loading the same path,
// which keeps the extension's id stable.
func syncBrowserExtension(ctx context.Context, l *slog.Logger, cfg *config.AgentEngineConfig, token string) {
	if token == "" {
		l.WarnContext(ctx, "browser extension not installed: the pairing token is unavailable")
		return
	}
	dir, err := store.ExtensionDir()
	if err != nil {
		l.WarnContext(ctx, "browser extension not installed", "error", err)
		return
	}

	changed, err := browserext.Install(dir, browserext.Config{
		DaemonURL: browserDaemonURL(cfg),
		Token:     token,
	})
	if err != nil {
		l.WarnContext(ctx, "browser extension install failed", "dir", dir, "error", err)
		return
	}
	id, _ := browserext.ExtensionID(dir)
	l.InfoContext(ctx, "browser extension ready",
		"dir", dir, "extension_id", id, "updated", changed,
		"note", "if updated is true, click Reload on the extension in chrome://extensions")
}

// browserDaemonURL is the address the extension dials. A wildcard bind address
// is not dialable, so it becomes localhost: the extension lives in the same
// machine's browser as the local daemon, or reaches a remote one by its name.
func browserDaemonURL(cfg *config.AgentEngineConfig) string {
	host := strings.TrimSpace(cfg.Host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Port)
}
