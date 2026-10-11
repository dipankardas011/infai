package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/glue"
)

func TestFormatTokenState(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	soon := now.Add(90 * time.Second)
	later := now.Add(3*time.Hour + 52*time.Minute)

	cases := []struct {
		name string
		view glue.MCPServerView
		want string
	}{
		{"a server without oauth", glue.MCPServerView{Auth: "none", HasToken: true}, "-"},
		{"a bearer token", glue.MCPServerView{Auth: "bearerToken"}, "-"},
		{"oauth without a token", glue.MCPServerView{Auth: "oauth"}, "no token"},
		{"expired", glue.MCPServerView{Auth: "oauth", HasToken: true, TokenExpiry: &expired}, "expired"},
		{"less than a minute left", glue.MCPServerView{Auth: "oauth", HasToken: true, TokenExpiry: &soon}, "2026-10-11 10:01Z (1m left)"},
		{"hours left", glue.MCPServerView{Auth: "oauth", HasToken: true, TokenExpiry: &later}, "2026-10-11 13:52Z (3h52m left)"},
		{"a token that never expires", glue.MCPServerView{Auth: "oauth", HasToken: true}, "no expiry"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTokenState(tt.view, now); got != tt.want {
				t.Fatalf("formatTokenState = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTokenLeft(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:             "under a minute",
		59 * time.Second:             "under a minute",
		90 * time.Second:             "1m",
		time.Hour:                    "1h00m",
		3*time.Hour + 52*time.Minute: "3h52m",
		25*time.Hour + 5*time.Minute: "25h05m",
	}
	for left, want := range cases {
		if got := formatTokenLeft(left); got != want {
			t.Errorf("formatTokenLeft(%s) = %q, want %q", left, got, want)
		}
	}
}

func TestMCPRow(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	expiry := now.Add(2 * time.Hour)
	row := mcpRow(glue.MCPServerView{
		Name:        "grafana",
		Transport:   "streamable-http",
		URL:         "https://grafana.example/mcp",
		Auth:        "oauth",
		HasToken:    true,
		TokenExpiry: &expiry,
	}, now)

	for _, want := range []string{"grafana", "streamable-http", "oauth", "https://grafana.example/mcp", "2026-10-11 12:00Z (2h00m left)"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not contain %q", row, want)
		}
	}
}
