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

	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type ToolsConfig struct {
	MCPServers   map[string]contracts.MCPServerConfig
	HITLPolicies map[string]*auditor.HITLPolicy
}

type toolsFileConfig struct {
	MCPServers   map[string]contracts.MCPServerConfig  `json:"mcpServers"`
	HITLPolicies map[string]contracts.HITLPolicyConfig `json:"hitlPolicies,omitempty"`
}

func LoadToolConfig() (ToolsConfig, error) {
	root, err := Root()
	if err != nil {
		return ToolsConfig{}, err
	}
	f, err := os.Open(filepath.Join(root, toolsFileName))
	if os.IsNotExist(err) {
		return ToolsConfig{HITLPolicies: map[string]*auditor.HITLPolicy{auditor.DefaultHITLPolicyName: auditor.NewDefaultHITLPolicy()}}, nil
	}
	if err != nil {
		// A missing file is MCP switched off; a file that cannot be read is a
		// problem with the file, and the operator needs the reason.
		return ToolsConfig{}, fmt.Errorf("store: read %s: %w", toolsFileName, err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var config *toolsFileConfig
	if err := decoder.Decode(&config); err != nil || config == nil {
		if err == nil {
			err = errors.New("the file holds no object")
		}
		return ToolsConfig{}, fmt.Errorf("store: invalid %s JSON: %w", toolsFileName, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ToolsConfig{}, fmt.Errorf("store: %s contains trailing JSON or invalid data", toolsFileName)
	}

	for name := range config.MCPServers {
		if err := ValidateMCPServer(name, config.MCPServers[name]); err != nil {
			return ToolsConfig{}, fmt.Errorf("store: %w", err)
		}
	}
	policies := map[string]*auditor.HITLPolicy{
		auditor.DefaultHITLPolicyName: auditor.NewDefaultHITLPolicy(),
	}
	for name, decisions := range config.HITLPolicies {
		if name == auditor.DefaultHITLPolicyName {
			continue
		}
		if err := ValidatePolicy(name, decisions); err != nil {
			return ToolsConfig{}, fmt.Errorf("store: %w", err)
		}
		policy, err := auditor.TranslateToHITLPolicy(name, decisions)
		if err != nil {
			return ToolsConfig{}, fmt.Errorf("store: %w", err)
		}
		policies[name] = policy
	}
	return ToolsConfig{MCPServers: config.MCPServers, HITLPolicies: policies}, nil
}

// SaveToolConfig replaces tools.json. Every server is validated first, so a
// rejected edit leaves the file exactly as it was.
func SaveToolConfig(config ToolsConfig) error {
	for name := range config.MCPServers {
		if err := ValidateMCPServer(name, config.MCPServers[name]); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	disk := toolsFileConfig{MCPServers: config.MCPServers, HITLPolicies: make(map[string]contracts.HITLPolicyConfig)}
	for name, policy := range config.HITLPolicies {
		if name == auditor.DefaultHITLPolicyName {
			continue
		}
		if policy == nil {
			return fmt.Errorf("store: policy %q is nil", name)
		}
		decisions := make(contracts.HITLPolicyConfig, len(policy.ToolMappings))
		for tool, decision := range policy.ToolMappings {
			decisions[string(tool)] = decision.String()
		}
		if err := ValidatePolicy(name, decisions); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		disk.HITLPolicies[name] = decisions
	}

	root, err := Root()
	if err != nil {
		return err
	}
	path := filepath.Join(root, toolsFileName)
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}

	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal tools config: %w", err)
	}
	data = append(data, '\n')

	// The file carries bearer tokens, header values and OAuth tokens, so it is
	// owner-only.
	if err := atomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("store: write %s: %w", toolsFileName, err)
	}
	return nil
}

func ValidatePolicy(name string, pc contracts.HITLPolicyConfig) error {
	if !serverName.MatchString(name) {
		return fmt.Errorf("policy %q: name must contain only ASCII letters, digits, underscores or hyphens", name)
	}
	for tool, decision := range pc {
		switch decision {
		case string(auditor.DenyPolicy), string(auditor.AllowPolicy), string(auditor.HumanPolicy):
		default:
			return fmt.Errorf("policy %q, tool %q: decision must be allow, human, or deny (got %q)", name, tool, decision)
		}
	}
	return nil
}

// The names allowed in tools.json. A server name becomes part of every tool
// name the server exposes, an env name has to survive as NAME=value in a child
// process's environment, and a header name is an HTTP field-name token
// (§ RFC 9110 §5.1).
var (
	serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
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
	case contracts.MCPTransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("stdio requires a command")
		}
		// A NUL cannot be passed to exec, and a server whose config holds one
		// would fail at connect time, where the reason only reaches the log.
		if strings.ContainsRune(server.Command, 0) {
			return fmt.Errorf("stdio command must not contain a NUL")
		}
		for _, arg := range server.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("stdio arguments must not contain a NUL")
			}
		}
		for _, value := range server.Env {
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("environment values must not contain a NUL")
			}
		}
		if server.URL != "" || len(server.Headers) != 0 || server.BearerToken != "" || server.OAuth != nil {
			return fmt.Errorf("stdio must not specify HTTP fields")
		}
	case contracts.MCPTransportStreamableHTTP:
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
		return fmt.Errorf("type must be %s or %s", contracts.MCPTransportStdio, contracts.MCPTransportStreamableHTTP)
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
