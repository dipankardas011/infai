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
	if unsafePort(cfg.Port) {
		l.WarnContext(ctx, "browser extension cannot connect to this port",
			"port", cfg.Port,
			"reason", "Chrome blocks WebSocket connections to this port (net::ERR_UNSAFE_PORT; 6000 is X11)",
			"fix", "start the daemon on another port, e.g. INFAI_AGENT_PORT=7000")
	}
	id, _ := browserext.ExtensionID(dir)
	l.InfoContext(ctx, "browser extension ready",
		"dir", dir, "extension_id", id, "updated", changed,
		"note", "if updated is true, click Reload on the extension in chrome://extensions")
}

// unsafePort reports whether Chrome refuses connections to a port. The list is
// net/base/port_util.cc: below 1024, plus a set of well-known service ports.
func unsafePort(port int) bool {
	if port > 0 && port < 1024 {
		return true
	}
	switch port {
	case 2049, 3659, 4045, 5060, 5061, 6000, 6566, 6665, 6666, 6667, 6668, 6669, 6679, 6697, 10080:
		return true
	}
	return false
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
