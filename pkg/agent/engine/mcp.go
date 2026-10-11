package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

var ErrMCPOperation = errors.New("another MCP configuration or authorization operation is already running")

var ErrMCPNotFound = errors.New("MCP server is not configured")

var ErrMCPExists = errors.New("MCP server already exists")

var ErrMCPInvalid = errors.New("invalid MCP server configuration")

// ListMCPServers reports every configured server, with its stored token state.
func (e *InfaiAgentEngine) ListMCPServers(ctx context.Context) ([]glue.MCPServerView, error) {
	config, err := store.LoadToolConfig()
	if err != nil {
		return nil, err
	}
	views := make([]glue.MCPServerView, 0, len(config.MCPServers))
	for _, name := range slices.Sorted(maps.Keys(config.MCPServers)) {
		views = append(views, e.mcpServerView(ctx, name, config.MCPServers[name]))
	}
	return views, nil
}

// AddMCPServer stores a new server. An existing name is ErrMCPExists.
func (e *InfaiAgentEngine) AddMCPServer(ctx context.Context, input glue.MCPServerInput) (glue.MCPServerView, error) {
	if !e.mcpMu.TryLock() {
		return glue.MCPServerView{}, ErrMCPOperation
	}
	defer e.mcpMu.Unlock()
	config, err := store.LoadToolConfig()
	if err != nil {
		return glue.MCPServerView{}, err
	}
	if _, ok := config.MCPServers[input.Name]; ok {
		return glue.MCPServerView{}, fmt.Errorf("%w: %q", ErrMCPExists, input.Name)
	}
	if err := store.ValidateMCPServer(input.Name, input.Config); err != nil {
		return glue.MCPServerView{}, fmt.Errorf("%w: %v", ErrMCPInvalid, err)
	}
	if config.MCPServers == nil {
		config.MCPServers = make(map[string]contracts.MCPServerConfig)
	}
	config.MCPServers[input.Name] = input.Config
	if err := store.SaveToolConfig(config); err != nil {
		return glue.MCPServerView{}, err
	}
	return e.mcpServerView(ctx, input.Name, input.Config), nil
}

// EditMCPServer replaces an existing server. A missing name is ErrMCPNotFound.
// It never renames: the name is the key.
//
// A credential of the same kind left empty keeps its stored value, because a
// client never reads a credential back and an empty field therefore means
// "unchanged". Clearing one means removing and re-adding the server.
func (e *InfaiAgentEngine) EditMCPServer(ctx context.Context, input glue.MCPServerInput) (glue.MCPServerView, error) {
	if !e.mcpMu.TryLock() {
		return glue.MCPServerView{}, ErrMCPOperation
	}
	defer e.mcpMu.Unlock()
	config, err := store.LoadToolConfig()
	if err != nil {
		return glue.MCPServerView{}, err
	}
	stored, ok := config.MCPServers[input.Name]
	if !ok {
		return glue.MCPServerView{}, fmt.Errorf("%w: %q", ErrMCPNotFound, input.Name)
	}
	edited := mergeMCPCredentials(stored, input.Config)
	if err := store.ValidateMCPServer(input.Name, edited); err != nil {
		return glue.MCPServerView{}, fmt.Errorf("%w: %v", ErrMCPInvalid, err)
	}
	config.MCPServers[input.Name] = edited
	if err := store.SaveToolConfig(config); err != nil {
		return glue.MCPServerView{}, err
	}
	return e.mcpServerView(ctx, input.Name, edited), nil
}

// mergeMCPCredentials keeps a stored credential that the edit left empty. An
// edit that names no credential at all keeps the kind the server already uses,
// because the client cannot read a credential back to send it again. Naming a
// different kind replaces the config outright, and the store's validation then
// rejects anything that mixes two.
func mergeMCPCredentials(stored, edited contracts.MCPServerConfig) contracts.MCPServerConfig {
	kind := mcpAuth(edited)
	if kind == contracts.MCPAuthNone {
		kind = mcpAuth(stored)
	}
	switch kind {
	case contracts.MCPAuthNone:
		// A stdio server keeps its credentials in the environment of its own
		// subprocess, and the client cannot read those back either.
		if edited.Transport == "stdio" && len(edited.Env) == 0 {
			edited.Env = stored.Env
		}
	case contracts.MCPAuthBearer:
		if edited.BearerToken == "" {
			edited.BearerToken = stored.BearerToken
		}
	case contracts.MCPAuthHeaders:
		if len(edited.Headers) == 0 {
			edited.Headers = stored.Headers
		}
	case contracts.MCPAuthOAuth:
		if edited.OAuth == nil || stored.OAuth == nil {
			if edited.OAuth == nil {
				edited.OAuth = stored.OAuth
			}
			break
		}
		oauth := *edited.OAuth
		if oauth.RedirectURL == "" {
			oauth.RedirectURL = stored.OAuth.RedirectURL
		}
		if oauth.ClientID == "" {
			oauth.ClientID = stored.OAuth.ClientID
		}
		if oauth.ClientSecret == "" {
			oauth.ClientSecret = stored.OAuth.ClientSecret
		}
		if oauth.Issuer == "" {
			oauth.Issuer = stored.OAuth.Issuer
		}
		if len(oauth.Scopes) == 0 {
			oauth.Scopes = stored.OAuth.Scopes
		}
		// A stored token belongs to the endpoint and client it was minted for,
		// so it survives only an edit that changed neither.
		if edited.URL == stored.URL && sameMCPOAuthClient(oauth, *stored.OAuth) {
			oauth = *stored.OAuth
		}
		edited.OAuth = &oauth
	}
	return edited
}

// sameMCPOAuthClient reports whether two oauth blocks describe the same client
// registration, ignoring the token that may be stored alongside it.
func sameMCPOAuthClient(a, b contracts.MCPOAuthConfig) bool {
	return a.RedirectURL == b.RedirectURL && a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret && a.Issuer == b.Issuer && slices.Equal(a.Scopes, b.Scopes)
}

// RemoveMCPServer deletes a server and its stored authorization. A missing name
// is ErrMCPNotFound.
func (e *InfaiAgentEngine) RemoveMCPServer(ctx context.Context, name string) error {
	if !e.mcpMu.TryLock() {
		return ErrMCPOperation
	}
	defer e.mcpMu.Unlock()
	config, err := store.LoadToolConfig()
	if err != nil {
		return err
	}
	if _, ok := config.MCPServers[name]; !ok {
		return fmt.Errorf("%w: %q", ErrMCPNotFound, name)
	}
	delete(config.MCPServers, name)
	return store.SaveToolConfig(config)
}

// ProbeMCPServer connects to one configured server and reports what it offers.
func (e *InfaiAgentEngine) ProbeMCPServer(ctx context.Context, name string) (glue.MCPProbeOutput, error) {
	config, err := store.LoadToolConfig()
	if err != nil {
		return glue.MCPProbeOutput{}, err
	}
	cfg, ok := config.MCPServers[name]
	if !ok {
		return glue.MCPProbeOutput{}, fmt.Errorf("%w: %q", ErrMCPNotFound, name)
	}

	// The workspace belongs to a session; a config probe only needs a directory.
	cwd, err := os.Getwd()
	if err != nil {
		return glue.MCPProbeOutput{}, err
	}
	manager, err := actuators.NewMCPManager(ctx, cwd, e.bgLogger, map[string]contracts.MCPServerConfig{name: cfg}, nil)
	if err != nil {
		return glue.MCPProbeOutput{}, err
	}
	defer manager.Close()

	return glue.MCPProbeOutput{
		Server:    name,
		Tools:     len(manager.Tools()),
		Prompts:   len(manager.Prompts()),
		Resources: len(manager.Resources()),
	}, nil
}

// mcpServerView renders one configured server, resolving its stored token
// state. It never copies a credential into the view.
func (e *InfaiAgentEngine) mcpServerView(_ context.Context, name string, cfg contracts.MCPServerConfig) glue.MCPServerView {
	view := glue.MCPServerView{
		Name:           name,
		Transport:      cfg.Transport,
		Auth:           mcpAuth(cfg),
		TimeoutSeconds: cfg.TimeoutSeconds,
	}
	if cfg.Transport == "stdio" {
		view.Command = cfg.Command
		view.Args = cfg.Args
	} else {
		view.URL = cfg.URL
	}
	if cfg.OAuth == nil {
		return view
	}
	view.HasToken = cfg.OAuth.AccessToken != ""
	if !cfg.OAuth.Expiry.IsZero() {
		expiry := cfg.OAuth.Expiry
		view.TokenExpiry = &expiry
	}
	return view
}

func mcpAuth(cfg contracts.MCPServerConfig) contracts.MCPAuthKind {
	switch {
	case cfg.OAuth != nil:
		return contracts.MCPAuthOAuth
	case cfg.BearerToken != "":
		return contracts.MCPAuthBearer
	case len(cfg.Headers) > 0:
		return contracts.MCPAuthHeaders
	default:
		return contracts.MCPAuthNone
	}
}

// LoginMCPServer runs one interactive authorization for a configured MCP server
// and stores the resulting token under that server's name. The URL the user must
// open arrives through onUpdate; the call blocks until the browser comes back,
// the flow fails, or the context ends.
func (e *InfaiAgentEngine) LoginMCPServer(ctx context.Context, input glue.MCPLoginInput, onUpdate func(glue.MCPLoginOutput) error) error {
	if input.Server == "" {
		return errors.New("MCP server name is required")
	}
	if onUpdate == nil {
		return errors.New("MCP login update callback is required")
	}
	if !e.mcpMu.TryLock() {
		return ErrMCPOperation
	}
	defer e.mcpMu.Unlock()
	loginCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()

	select {
	case <-e.stopCh:
		return harnessErr.ErrEngineShuttingDown
	default:
	}

	config, err := store.LoadToolConfig()
	if err != nil {
		return err
	}
	server, ok := config.MCPServers[input.Server]
	if !ok {
		return fmt.Errorf("MCP server %q is not configured", input.Server)
	}
	if server.OAuth == nil {
		return fmt.Errorf("MCP server %q has no oauth block to authorize", input.Server)
	}
	if server.Transport != "streamable-http" {
		return fmt.Errorf("MCP server %q does not use OAuth over HTTP", input.Server)
	}

	oauth, err := actuators.AuthorizeMCPOAuth(loginCtx, server, e.bgLogger, func(update actuators.MCPAuthUpdate) error {
		return onUpdate(glue.MCPLoginOutput{Status: contracts.ProviderAuthPending, URL: update.URL, Callback: update.Callback})
	})
	if err != nil {
		return err
	}
	server.OAuth = oauth
	config.MCPServers[input.Server] = server
	if err := store.SaveToolConfig(config); err != nil {
		return err
	}
	e.bgLogger.InfoContext(ctx, "MCP authorization completed", "server", input.Server)
	return onUpdate(glue.MCPLoginOutput{Status: contracts.ProviderAuthComplete})
}
