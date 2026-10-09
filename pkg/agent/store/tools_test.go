package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func writeMCPTestConfig(t *testing.T, data string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tools.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMCPConfigMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	config, err := LoadToolConfig()
	if err != nil || len(config.MCPServers) != 0 {
		t.Fatalf("missing config: got %#v, %v", config, err)
	}
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("missing config should not create root: %v", err)
	}
}

func TestLoadMCPConfigValid(t *testing.T) {
	want := contracts.ToolsConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"Local_1-test": {Transport: "stdio", Command: "tool", Args: []string{"--flag"}, Env: map[string]string{"STATIC": "value"}},
		"remote":       {Transport: "streamable-http", URL: "https://example.com/mcp?version=1", Headers: map[string]string{"X-Token": "secret"}, BearerTokenEnv: "HOST_TOKEN", TimeoutSeconds: 120},
	}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := writeMCPTestConfig(t, string(data)+"\n \t")
	t.Setenv("HOST_TOKEN", "resolved-secret")
	got, err := LoadToolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data)+"\n \t" {
		t.Fatalf("loader changed config: %v", err)
	}
}

func TestLoadMCPConfigDecodeErrors(t *testing.T) {
	for name, data := range map[string]string{
		"malformed": `{`,
		// The old list form and a per-entry name are the two migration traps.
		"list servers": `{"mcpServers":[{"type":"stdio"}]}`,
		"name field":   `{"mcpServers":{"local":{"name":"secret-token"}}}`,
		"trailing":     `{} secret-token`,
	} {
		t.Run(name, func(t *testing.T) {
			writeMCPTestConfig(t, data)
			got, err := LoadToolConfig()
			if err == nil {
				t.Fatal("expected decode error")
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("decode error leaked secret: %v", err)
			}
			if len(got.MCPServers) != 0 {
				t.Fatal("returned partial config")
			}
		})
	}
}

func TestLoadMCPConfigInvalidServer(t *testing.T) {
	stdio := contracts.MCPServerConfig{Transport: "stdio", Command: "tool"}
	http := contracts.MCPServerConfig{Transport: "streamable-http", URL: "https://example.com/mcp"}
	tests := []struct {
		name string
		key  string
		base contracts.MCPServerConfig
		edit func(*contracts.MCPServerConfig)
	}{
		{"empty name", "", stdio, func(*contracts.MCPServerConfig) {}},
		{"unknown transport", "local", stdio, func(s *contracts.MCPServerConfig) { s.Transport = "secret-token" }},
		{"blank command", "local", stdio, func(s *contracts.MCPServerConfig) { s.Command = " \t" }},
		{"http command", "remote", http, func(s *contracts.MCPServerConfig) { s.Command = "secret-token" }},
		{"wrong scheme", "remote", http, func(s *contracts.MCPServerConfig) { s.URL = "ftp://secret-token.example" }},
		{"nul bearer reference", "remote", http, func(s *contracts.MCPServerConfig) { s.BearerTokenEnv = "secret-token\x00" }},
		{"authorization conflict", "remote", http, func(s *contracts.MCPServerConfig) {
			s.Headers = map[string]string{"aUtHoRiZaTiOn": "Bearer secret-token"}
			s.BearerTokenEnv = "TOKEN"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.base
			tt.edit(&server)
			data, err := json.Marshal(contracts.ToolsConfig{MCPServers: map[string]contracts.MCPServerConfig{tt.key: server}})
			if err != nil {
				t.Fatal(err)
			}
			writeMCPTestConfig(t, string(data))
			got, err := LoadToolConfig()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), "MCP server") || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("unsafe or missing server context: %v", err)
			}
			if len(got.MCPServers) != 0 {
				t.Fatal("returned partial config")
			}
		})
	}
}
