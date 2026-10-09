package contracts

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
	BearerTokenEnv string            `json:"bearerTokenEnv,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}
