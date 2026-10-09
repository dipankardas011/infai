package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func LoadToolConfig() (contracts.ToolsConfig, error) {
	root, err := Root()
	if err != nil {
		return contracts.ToolsConfig{}, err
	}
	f, err := os.Open(filepath.Join(root, "tools.json"))
	if os.IsNotExist(err) {
		return contracts.ToolsConfig{}, nil
	}
	if err != nil {
		return contracts.ToolsConfig{}, fmt.Errorf("store: read tools config failed")
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var config *contracts.ToolsConfig
	if err := decoder.Decode(&config); err != nil || config == nil {
		return contracts.ToolsConfig{}, fmt.Errorf("store: invalid tools config JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return contracts.ToolsConfig{}, fmt.Errorf("store: tools config contains trailing JSON or invalid data")
	}

	for _, name := range slices.Sorted(maps.Keys(config.MCPServers)) {
		if err := validateMCPServerName(name); err != nil {
			return contracts.ToolsConfig{}, fmt.Errorf("store: MCP server %q: %w", name, err)
		}
		if err := validateMCPServer(config.MCPServers[name]); err != nil {
			return contracts.ToolsConfig{}, fmt.Errorf("store: MCP server %q: %w", name, err)
		}
	}
	return *config, nil
}

// The names allowed in tools.json. A server name becomes part of every tool
// name the server exposes, an env name must survive as NAME=value, and a header
// name is an HTTP field-name token (§ RFC 9110 §5.1).
var (
	serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envVarName = regexp.MustCompile(`^[^=\x00]+$`)
	headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

func validateMCPServerName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case !serverName.MatchString(name):
		return errors.New("name must contain only ASCII letters, digits, underscores or hyphens")
	}
	return nil
}

func validateMCPServer(server contracts.MCPServerConfig) error {
	if server.TimeoutSeconds < 0 || int64(server.TimeoutSeconds) > math.MaxInt64/int64(time.Second) {
		return fmt.Errorf("timeoutSeconds must fit a nonnegative duration")
	}
	switch server.Transport {
	case "stdio":
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("stdio requires a command")
		}
		if server.URL != "" || len(server.Headers) != 0 || server.BearerTokenEnv != "" {
			return fmt.Errorf("stdio must not specify HTTP fields")
		}
	case "streamable-http":
		if server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
			return fmt.Errorf("streamable-http must not specify subprocess fields")
		}
		u, err := url.Parse(server.URL)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.Contains(server.URL, "#") {
			return fmt.Errorf("URL must be HTTP or HTTPS with a hostname and no userinfo or fragment")
		}
	default:
		return fmt.Errorf("type must be stdio or streamable-http")
	}
	for key := range server.Env {
		if !envVarName.MatchString(key) {
			return errors.New("invalid env variable name")
		}
	}
	if server.BearerTokenEnv != "" && !envVarName.MatchString(server.BearerTokenEnv) {
		return errors.New("invalid bearerTokenEnv variable reference")
	}
	for key, value := range server.Headers {
		if !headerName.MatchString(key) || strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid HTTP header name or value")
		}
		if strings.EqualFold(key, "Authorization") && server.BearerTokenEnv != "" {
			return errors.New("Authorization and bearerTokenEnv cannot both be supplied")
		}
	}
	return nil
}
