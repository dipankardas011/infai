package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	for name := range config.MCPServers {
		if err := ValidateMCPServer(name, config.MCPServers[name]); err != nil {
			return contracts.ToolsConfig{}, fmt.Errorf("store: %w", err)
		}
	}
	return *config, nil
}

// SaveToolConfig replaces tools.json. Every server is validated first, so a
// rejected edit leaves the file exactly as it was.
func SaveToolConfig(config contracts.ToolsConfig) error {
	for name := range config.MCPServers {
		if err := ValidateMCPServer(name, config.MCPServers[name]); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}

	root, err := Root()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "tools.json")
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal tools config: %w", err)
	}
	data = append(data, '\n')

	return writeToolsFile(path, data)
}

// tools.json can carry bearer tokens and header values, so it is owner-only.
func writeToolsFile(path string, data []byte) error {
	if err := atomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("store: write tools config: %w", err)
	}
	return nil
}

// The names allowed in tools.json. A server name becomes part of every tool
// name the server exposes, an env name must survive as NAME=value, and a header
// name is an HTTP field-name token (§ RFC 9110 §5.1).
var (
	serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envVarName = regexp.MustCompile(`^[^=\x00]+$`)
	headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// ValidateMCPServer reports whether one server entry is usable, naming the
// server in the message. Reading a config file, writing one and accepting a
// management request all judge an entry by these rules.
func ValidateMCPServer(name string, server contracts.MCPServerConfig) error {
	if err := validateMCPServerName(name); err != nil {
		return fmt.Errorf("MCP server %q: %w", name, err)
	}
	if err := validateMCPServer(server); err != nil {
		return fmt.Errorf("MCP server %q: %w", name, err)
	}
	return nil
}

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
		if server.URL != "" || len(server.Headers) != 0 || server.BearerToken != "" || server.OAuth != nil {
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
		if server.OAuth != nil {
			if strings.ContainsAny(server.OAuth.AccessToken, "\x00\r\n") {
				return errors.New("invalid oauth accessToken")
			}
			if server.OAuth.ClientSecret != "" && server.OAuth.ClientID == "" {
				return errors.New("oauth clientSecret requires clientId")
			}
			if strings.ContainsAny(server.OAuth.ClientID, "\x00\r\n") {
				return errors.New("invalid oauth clientId")
			}
			if strings.ContainsAny(server.OAuth.ClientSecret, "\x00\r\n") {
				return errors.New("invalid oauth clientSecret")
			}
			if server.OAuth.RedirectURL != "" {
				ru, err := url.Parse(server.OAuth.RedirectURL)
				if err != nil || ru == nil || ru.Scheme != "http" || !slices.Contains([]string{"127.0.0.1", "::1", "localhost"}, ru.Hostname()) || ru.Port() == "" || ru.User != nil || ru.Fragment != "" {
					return errors.New("oauth redirectUrl must be a loopback HTTP URL")
				}
			}
			if server.OAuth.Issuer != "" {
				iu, err := url.Parse(server.OAuth.Issuer)
				if err != nil || iu == nil || (iu.Scheme != "http" && iu.Scheme != "https") || iu.Hostname() == "" || iu.User != nil || iu.Fragment != "" {
					return errors.New("oauth issuer must be an HTTP or HTTPS URL")
				}
			}
			for _, scope := range server.OAuth.Scopes {
				if scope == "" || scope != strings.TrimSpace(scope) || strings.ContainsAny(scope, " \t\r\n") {
					return errors.New("invalid oauth scope")
				}
			}
		}
	default:
		return fmt.Errorf("type must be stdio or streamable-http")
	}
	for key := range server.Env {
		if !envVarName.MatchString(key) {
			return errors.New("invalid env variable name")
		}
	}
	if server.BearerToken != "" && strings.ContainsAny(server.BearerToken, "\x00\r\n") {
		return errors.New("invalid bearer token")
	}
	for key, value := range server.Headers {
		if !headerName.MatchString(key) || strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid HTTP header name or value")
		}
		if strings.EqualFold(key, "Authorization") && server.BearerToken != "" {
			return errors.New("Authorization and bearerToken cannot both be supplied")
		}
		if strings.EqualFold(key, "Authorization") && server.OAuth != nil {
			return errors.New("oauth and an Authorization header cannot both be supplied")
		}
	}
	if server.OAuth != nil && server.BearerToken != "" {
		return errors.New("oauth and bearerToken cannot both be supplied")
	}
	return nil
}
