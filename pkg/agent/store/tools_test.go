package store

import (
	"bytes"
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
	want := toolsFileConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"Local_1-test": {Transport: "stdio", Command: "tool", Args: []string{"--flag"}, Env: map[string]string{"STATIC": "value"}},
		"remote":       {Transport: "streamable-http", URL: "https://example.com/mcp?version=1", Headers: map[string]string{"X-Token": "secret"}, BearerToken: "resolved-secret", TimeoutSeconds: 120},
		"remote-oauth": {Transport: "streamable-http", URL: "https://example.com/mcp", OAuth: &contracts.MCPOAuthConfig{RedirectURL: "http://127.0.0.1:6000/oauth/callback", ClientID: "harness", ClientSecret: "client-secret", Scopes: []string{"mcp.read"}}},
	}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := writeMCPTestConfig(t, string(data)+"\n \t")
	got, err := LoadToolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.MCPServers, want.MCPServers) {
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
		{"nul bearer token", "remote", http, func(s *contracts.MCPServerConfig) { s.BearerToken = "secret-token\x00" }},
		{"authorization conflict", "remote", http, func(s *contracts.MCPServerConfig) {
			s.Headers = map[string]string{"aUtHoRiZaTiOn": "Bearer secret-token"}
			s.BearerToken = "TOKEN"
		}},
		{"oauth on stdio", "remote", stdio, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{}
		}},
		{"oauth with bearerToken", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{}
			s.BearerToken = "TOKEN"
		}},
		{"oauth with authorization header", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{}
			s.Headers = map[string]string{"authorization": "Bearer secret-token"}
		}},
		{"oauth secret without clientId", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{ClientSecret: "CLIENT_SECRET"}
		}},
		{"oauth invalid clientSecret", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{ClientID: "harness", ClientSecret: "secret-token\x00"}
		}},
		{"oauth redirect not loopback", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{ClientID: "harness", RedirectURL: "https://secret-token.example/callback"}
		}},
		{"oauth issuer not http", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{ClientID: "harness", Issuer: "ftp://secret-token.example"}
		}},
		{"oauth invalid scope", "remote", http, func(s *contracts.MCPServerConfig) {
			s.OAuth = &contracts.MCPOAuthConfig{ClientID: "harness", Scopes: []string{"two words"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.base
			tt.edit(&server)
			data, err := json.Marshal(toolsFileConfig{MCPServers: map[string]contracts.MCPServerConfig{tt.key: server}})
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

func TestSaveToolConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := toolsFileConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"local":  {Transport: "stdio", Command: "tool", Args: []string{"--flag"}, Env: map[string]string{"STATIC": "value"}},
		"remote": {Transport: "streamable-http", URL: "https://example.com/mcp", BearerToken: "stored-secret", TimeoutSeconds: 30},
	}}
	if err := SaveToolConfig(ToolsConfig{MCPServers: want.MCPServers}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadToolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.MCPServers, want.MCPServers) {
		t.Fatalf("got %#v, want %#v", got, want)
	}

	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("tools.json mode = %v, want 0600", mode)
	}
}

func TestSaveToolConfigRejectsBeforeWriting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tools.json")
	invalid := toolsFileConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"bad": {Transport: "carrier-pigeon", Command: "tool"},
	}}

	// Nothing is on disk yet: a rejected save must not create the file.
	if err := SaveToolConfig(ToolsConfig{MCPServers: invalid.MCPServers}); err == nil {
		t.Fatal("expected a validation error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected save created tools.json: %v", err)
	}

	valid := toolsFileConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"local": {Transport: "stdio", Command: "tool"},
	}}
	if err := SaveToolConfig(ToolsConfig{MCPServers: valid.MCPServers}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A valid file is present: a rejected save must leave it byte-identical.
	if err := SaveToolConfig(ToolsConfig{MCPServers: invalid.MCPServers}); err == nil {
		t.Fatal("expected a validation error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("rejected save changed tools.json:\nbefore %q\nafter  %q", before, after)
	}
}
