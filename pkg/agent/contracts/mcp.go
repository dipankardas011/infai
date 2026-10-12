package contracts

import "time"

// ToolsConfig is the harness's tool configuration, keyed by server name. The
// name is what tool names and error messages use; it is never repeated inside
// the server's own entry.
type ToolsConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

type MCPServerConfig struct {
	Transport      string            `json:"type"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	BearerToken    string            `json:"bearerToken,omitempty"`
	OAuth          *MCPOAuthConfig   `json:"oauth,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

const (
	MCPTransportStdio          = "stdio"
	MCPTransportStreamableHTTP = "streamable-http"
)

// MCPAuthKind is how a configured MCP server authenticates. The values are the
// names of the config fields they stand for, so a view can be read against the
// entry it describes.
type MCPAuthKind string

const (
	MCPAuthNone    MCPAuthKind = "none"
	MCPAuthBearer  MCPAuthKind = "bearerToken"
	MCPAuthHeaders MCPAuthKind = "headers"
	MCPAuthOAuth   MCPAuthKind = "oauth"
)

// MCPOAuthConfig opts a streamable-http server into OAuth 2.1. An empty block
// means dynamic client registration with PKCE and no preregistration; a
// ClientID means the client is preregistered with the authorization server.
type MCPOAuthConfig struct {
	RedirectURL  string    `json:"redirectUrl,omitempty"`
	ClientID     string    `json:"clientId,omitempty"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	AccessToken  string    `json:"accessToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

// MCPServerStatus is a configured MCP server a session could not use, and the
// reason. A session reports it so a client can explain a short tool list instead
// of leaving the operator to compare logs.
type MCPServerStatus struct {
	Server string `json:"server"`
	Reason string `json:"reason"`
}

type MCPPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type MCPPrompt struct {
	Server      string              `json:"server"`
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Arguments   []MCPPromptArgument `json:"arguments,omitempty"`
}

type MCPResource struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type MCPResourceRead struct {
	Server   string `json:"server"`
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}
