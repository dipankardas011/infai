package glue

import (
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// MCPLoginInput names the configured server to authorize.
type MCPLoginInput struct {
	Server string `json:"server"`
}

// MCPServerInput is one configured server, as the management commands send it.
type MCPServerInput struct {
	Name   string                    `json:"name"`
	Config contracts.MCPServerConfig `json:"config"`
}

// MCPServerView is one configured server as the management commands render it.
// It deliberately carries no credential: no header values, no bearer token, no
// client secret, no OAuth token.
type MCPServerView struct {
	Name           string                `json:"name"`
	Transport      string                `json:"transport"`
	Auth           contracts.MCPAuthKind `json:"auth"`
	TimeoutSeconds int                   `json:"timeout_seconds,omitempty"`
	// URL describes a streamable-http server, and Command with Args describes a
	// stdio server's subprocess. Both are what an edit has to show again.
	URL         string     `json:"url,omitempty"`
	Command     string     `json:"command,omitempty"`
	Args        []string   `json:"args,omitempty"`
	HasToken    bool       `json:"has_token"`
	TokenExpiry *time.Time `json:"token_expiry,omitempty"`
}

// MCPProbeOutput is what connecting to one server found. The counts are zero
// both for a server that was reached and offers nothing and for one that was
// never reached, so Reason tells them apart.
type MCPProbeOutput struct {
	Server    string `json:"server"`
	Tools     int    `json:"tools"`
	Prompts   int    `json:"prompts"`
	Resources int    `json:"resources"`
	// Reason is why the server could not be used, in terms the operator can act
	// on. It is empty when the connection was made.
	Reason string `json:"reason,omitempty"`
}

// MCPLoginOutput is one update while an MCP authorization is in progress. It
// reuses contracts.ProviderAuthStatus so the TUI's existing SSE consumer, built
// for LoginProviderOutput, works unchanged for MCP logins too.
type MCPLoginOutput struct {
	Status contracts.ProviderAuthStatus `json:"status"`
	// URL is the authorization URL the operator has to open, sent before the
	// flow blocks on the browser.
	URL string `json:"url,omitempty"`
	// Callback is the loopback URL the browser comes back to. It is reported
	// with the authorization URL, because an authorization server that refuses
	// the request names this exact value.
	Callback string `json:"callback,omitempty"`
	Error    string `json:"error,omitempty"`
}
