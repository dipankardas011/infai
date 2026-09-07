package models

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/dipankardas011/infai/internal/config"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

const (
	openAICodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexIssuer       = "https://auth.openai.com"
	openAICodexScope        = "openid profile email offline_access"
	openAICodexRedirectURI  = "http://localhost:1455/auth/callback"
	openAICodexDeviceReturn = "https://auth.openai.com/deviceauth/callback"
	openAICodexBaseURL      = "https://chatgpt.com/backend-api"
	openAICodexEndpoint     = openAICodexBaseURL + "/codex/responses"
)

// LoginMethod selects one of OpenAI's Codex subscription login flows.
type LoginMethod string

const (
	LoginMethodBrowser LoginMethod = "browser"
	LoginMethodDevice  LoginMethod = "device_code"
)

// LoginInteraction displays (and, for browser login, may open) a login URL.
// Providers deliberately do not import or invoke any UI/browser package.
type LoginInteraction func(contracts.ProviderLoginEvent) error

type openAICodexURLs struct {
	issuer, endpoint string
}

var defaultOpenAICodexURLs = openAICodexURLs{
	issuer: openAICodexIssuer, endpoint: openAICodexEndpoint,
}

// LoginOpenAICodex authenticates a ChatGPT Plus/Pro account. The returned Auth
// is ready to pass to ProviderStore.SetAuth.
func LoginOpenAICodex(ctx context.Context, method LoginMethod, interact LoginInteraction) (store.Auth, error) {
	return loginOpenAICodex(ctx, method, interact, http.DefaultClient, defaultOpenAICodexURLs)
}

func loginOpenAICodex(ctx context.Context, method LoginMethod, interact LoginInteraction, client *http.Client, urls openAICodexURLs) (store.Auth, error) {
	if interact == nil {
		return store.Auth{}, codexError("login interaction is required")
	}
	switch method {
	case LoginMethodBrowser:
		return browserCodexLogin(ctx, interact, client, urls)
	case LoginMethodDevice:
		return deviceCodexLogin(ctx, interact, client, urls)
	default:
		return store.Auth{}, codexError("unknown login method %q", method)
	}
}

func randomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func authorizationFlow(issuer string) (authURL, verifier, state string, err error) {
	verifier, err = randomBase64(64)
	if err != nil {
		return "", "", "", codexError("generate PKCE verifier: %v", err)
	}
	state, err = randomBase64(32)
	if err != nil {
		return "", "", "", codexError("generate OAuth state: %v", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {openAICodexClientID},
		"redirect_uri":               {openAICodexRedirectURI},
		"scope":                      {openAICodexScope},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"infai"},
	}
	return strings.TrimRight(issuer, "/") + "/oauth/authorize?" + params.Encode(), verifier, state, nil
}

type callbackResult struct {
	code string
	err  error
}

func browserCodexLogin(ctx context.Context, interact LoginInteraction, client *http.Client, urls openAICodexURLs) (store.Auth, error) {
	authURL, verifier, state, err := authorizationFlow(urls.issuer)
	if err != nil {
		return store.Auth{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return store.Auth{}, codexError("start callback server: %v", err)
	}
	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		var result callbackResult
		switch {
		case q.Get("state") != state:
			result.err = errors.New("OAuth state mismatch")
		case q.Get("error") != "":
			result.err = fmt.Errorf("provider rejected login: %s", q.Get("error"))
		case q.Get("code") == "":
			result.err = errors.New("callback missing authorization code")
		default:
			result.code = q.Get("code")
		}
		select {
		case results <- result:
		default:
		}
		if result.err != nil {
			http.Error(w, "OpenAI Codex login failed. Return to infai.", http.StatusBadRequest)
		} else {
			_, _ = io.WriteString(w, "OpenAI Codex login complete. You may close this window.")
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	if err := interact(contracts.ProviderLoginEvent{URL: authURL, ExpiresIn: 5 * time.Minute}); err != nil {
		return store.Auth{}, codexError("present authorization URL: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case <-waitCtx.Done():
		return store.Auth{}, codexWrap("browser login", waitCtx.Err())
	case result := <-results:
		if result.err != nil {
			return store.Auth{}, codexError("browser callback: %v", result.err)
		}
		return exchangeCodexToken(ctx, client, urls.issuer, result.code, verifier, openAICodexRedirectURI, "")
	}
}

func deviceCodexLogin(ctx context.Context, interact LoginInteraction, client *http.Client, urls openAICodexURLs) (store.Auth, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var start struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := postJSON(ctx, client, strings.TrimRight(urls.issuer, "/")+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": openAICodexClientID}, &start); err != nil {
		return store.Auth{}, codexError("start device login: %v", err)
	}
	interval, err := parseJSONSeconds(start.Interval)
	if start.DeviceAuthID == "" || start.UserCode == "" || err != nil || interval < 0 {
		return store.Auth{}, codexError("invalid device authorization response")
	}
	if interval == 0 {
		interval = 5
	}
	if err := interact(contracts.ProviderLoginEvent{URL: strings.TrimRight(urls.issuer, "/") + "/codex/device", UserCode: start.UserCode, ExpiresIn: 15 * time.Minute}); err != nil {
		return store.Auth{}, codexError("present device code: %v", err)
	}

	delay := time.Duration(interval) * time.Second
	for {
		var result struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		status, body, err := postJSONStatus(ctx, client, strings.TrimRight(urls.issuer, "/")+"/api/accounts/deviceauth/token", map[string]string{
			"device_auth_id": start.DeviceAuthID, "user_code": start.UserCode,
		}, &result)
		if err != nil {
			return store.Auth{}, codexError("poll device login: %v", err)
		}
		if status >= 200 && status < 300 {
			if result.AuthorizationCode == "" || result.CodeVerifier == "" {
				return store.Auth{}, codexError("invalid device token response")
			}
			return exchangeCodexToken(ctx, client, urls.issuer, result.AuthorizationCode, result.CodeVerifier, openAICodexDeviceReturn, "")
		}
		code := responseErrorCode(body)
		if code == "slow_down" {
			delay += 5 * time.Second
		} else if status != http.StatusForbidden && status != http.StatusNotFound && code != "deviceauth_authorization_pending" {
			return store.Auth{}, codexError("device login status %d: %s", status, sanitizeBody(body))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return store.Auth{}, codexWrap("device login", ctx.Err())
		case <-timer.C:
		}
	}
}

func parseJSONSeconds(raw json.RawMessage) (int, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(s))
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, value, dst any) error {
	status, body, err := postJSONStatus(ctx, client, endpoint, value, dst)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("status %d: %s", status, sanitizeBody(body))
	}
	return nil
}

func postJSONStatus(ctx context.Context, client *http.Client, endpoint string, value, dst any) (int, []byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && dst != nil {
		if err := json.Unmarshal(body, dst); err != nil {
			return resp.StatusCode, body, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, body, nil
}

type codexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeCodexToken(ctx context.Context, client *http.Client, issuer, code, verifier, redirectURI, priorAccount string) (store.Auth, error) {
	return requestCodexToken(ctx, client, issuer, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {openAICodexClientID}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI},
	}, priorAccount)
}

func refreshCodexToken(ctx context.Context, client *http.Client, issuer, refreshToken, priorAccount string) (store.Auth, error) {
	if refreshToken == "" {
		return store.Auth{}, codexError("refresh token is missing")
	}
	return requestCodexToken(ctx, client, issuer, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {openAICodexClientID}, "refresh_token": {refreshToken},
	}, priorAccount)
}

func requestCodexToken(ctx context.Context, client *http.Client, issuer string, form url.Values, priorAccount string) (store.Auth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(issuer, "/")+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return store.Auth{}, codexError("create token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return store.Auth{}, codexError("token request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		return store.Auth{}, codexError("token status %d: %s", resp.StatusCode, sanitizeBody(body))
	}
	var token codexTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return store.Auth{}, codexError("decode token response: %v", err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return store.Auth{}, codexError("token response missing required fields")
	}
	accountID := priorAccount
	for _, encoded := range []string{token.IDToken, token.AccessToken} {
		if id, err := extractCodexAccountID(encoded); err == nil && id != "" {
			accountID = id
			break
		}
	}
	if accountID == "" {
		return store.Auth{}, codexError("token does not contain a ChatGPT account ID")
	}
	return store.Auth{
		Type: store.AuthTypeOAuth, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ExpiresAt: time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second), AccountID: accountID,
	}, nil
}

func extractCodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	// This only extracts routing claims from a token received over authenticated
	// TLS; it does not authenticate the JWT, so signature verification is omitted.
	var claims struct {
		AccountID string `json:"chatgpt_account_id"`
		Auth      struct {
			AccountID     string `json:"chatgpt_account_id"`
			Organizations []struct {
				ID string `json:"id"`
			} `json:"organizations"`
		} `json:"https://api.openai.com/auth"`
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.AccountID != "" {
		return claims.AccountID, nil
	}
	if claims.Auth.AccountID != "" {
		return claims.Auth.AccountID, nil
	}
	if len(claims.Auth.Organizations) > 0 && claims.Auth.Organizations[0].ID != "" {
		return claims.Auth.Organizations[0].ID, nil
	}
	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID, nil
	}
	return "", errors.New("account ID claim is missing")
}

// DiscoverOpenAICodexModels fetches the account-specific catalog exposed by
// OpenAI's ChatGPT Codex backend. This is not the public Platform /v1/models API.
func DiscoverOpenAICodexModels(ctx context.Context, auth store.Auth) (map[string]store.Model, error) {
	return discoverOpenAICodexModels(ctx, http.DefaultClient, openAICodexBaseURL, auth)
}

type codexCatalogModel struct {
	Slug                    string `json:"slug"`
	DisplayName             string `json:"display_name"`
	DefaultReasoningLevel   string `json:"default_reasoning_level"`
	SupportedReasoningLevel []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	Visibility    string `json:"visibility"`
	ContextWindow *int   `json:"context_window"`
}

func discoverOpenAICodexModels(ctx context.Context, client *http.Client, baseURL string, auth store.Auth) (map[string]store.Model, error) {
	if auth.Type != store.AuthTypeOAuth || auth.AccessToken == "" || auth.AccountID == "" {
		return nil, codexError("OAuth authentication is required to discover models")
	}
	endpoint := strings.TrimSuffix(normalizeCodexEndpoint(baseURL), "/responses") + "/models"
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, codexError("parse discovery URL: %v", err)
	}
	query := requestURL.Query()
	query.Set("client_version", clientVersion())
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, codexError("create discovery request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", auth.AccountID)
	setCodexClientHeaders(req.Header)
	resp, err := client.Do(req)
	if err != nil {
		return nil, codexError("discover models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		return nil, codexError("discover models status %d: %s", resp.StatusCode, sanitizeBody(body))
	}
	var catalog struct {
		Models []codexCatalogModel `json:"models"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := dec.Decode(&catalog); err != nil {
		return nil, codexError("decode model catalog: %v", err)
	}
	if len(catalog.Models) == 0 {
		return nil, codexError("model catalog is empty")
	}
	models := make(map[string]store.Model, len(catalog.Models))
	for _, meta := range catalog.Models {
		if meta.Visibility != "list" {
			continue
		}
		if strings.TrimSpace(meta.Slug) == "" || meta.ContextWindow == nil || *meta.ContextWindow <= 0 {
			return nil, codexError("model catalog contains an invalid visible entry")
		}
		if _, exists := models[meta.Slug]; exists {
			return nil, codexError("model catalog contains duplicate API id %q", meta.Slug)
		}
		model := store.Model{Name: meta.Slug, DisplayName: meta.DisplayName, ContextWindow: *meta.ContextWindow, ReasoningEffort: meta.DefaultReasoningLevel}
		seen := make(map[string]bool, len(meta.SupportedReasoningLevel))
		for _, level := range meta.SupportedReasoningLevel {
			if strings.TrimSpace(level.Effort) == "" || seen[level.Effort] {
				return nil, codexError("model %q has invalid reasoning levels", meta.Slug)
			}
			seen[level.Effort] = true
			model.ThinkingModes = append(model.ThinkingModes, level.Effort)
		}
		model.Reasoning = len(model.ThinkingModes) > 0
		if model.ReasoningEffort != "" && !seen[model.ReasoningEffort] {
			return nil, codexError("model %q default reasoning level is unsupported", meta.Slug)
		}
		models[meta.Slug] = model
	}
	if len(models) == 0 {
		return nil, codexError("model catalog has no visible models")
	}
	return models, nil
}

type openAICodexProvider struct {
	mu     sync.RWMutex
	config store.Provider
	state  contracts.ProviderStateStore
}

func newOpenAICodexProvider(config store.Provider, state contracts.ProviderStateStore) (contracts.LLMProvider, error) {
	if state == nil {
		return nil, codexError("provider state store is required")
	}
	return &openAICodexProvider{config: config, state: state}, nil
}

func (p *openAICodexProvider) ID() string { return p.config.ID }

func (p *openAICodexProvider) Config() contracts.ProviderConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProviderConfig(p.config)
}

func (p *openAICodexProvider) Login(ctx context.Context, request contracts.ProviderLoginRequest) error {
	method := LoginMethod(request.Method)
	if method == "" {
		method = LoginMethodBrowser
	}
	auth, err := LoginOpenAICodex(ctx, method, request.Notify)
	if err != nil {
		return err
	}
	models, err := DiscoverOpenAICodexModels(ctx, auth)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	config := p.config
	config.Auth = auth
	config.Models = models
	if err := p.state.UpdateProvider(config); err != nil {
		return codexError("persist login and model catalog: %v", err)
	}
	p.config = config
	return nil
}

func (p *openAICodexProvider) Logout() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	config := p.config
	config.Auth = store.Auth{}
	if err := p.state.UpdateProvider(config); err != nil {
		return codexError("persist logout: %v", err)
	}
	p.config = config
	return nil
}

func (p *openAICodexProvider) RefreshModels(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	auth := p.config.Auth
	if auth.Type != store.AuthTypeOAuth || auth.RefreshToken == "" {
		return codexError("provider is not logged in")
	}
	if auth.AccessToken == "" || time.Until(auth.ExpiresAt) <= 5*time.Minute {
		refreshed, err := refreshCodexToken(ctx, http.DefaultClient, openAICodexIssuer, auth.RefreshToken, auth.AccountID)
		if err != nil {
			return err
		}
		auth = refreshed
	}
	models, err := DiscoverOpenAICodexModels(ctx, auth)
	if err != nil {
		return err
	}
	config := p.config
	config.Auth = auth
	config.Models = models
	if err := p.state.UpdateProvider(config); err != nil {
		return codexError("persist refreshed model catalog: %v", err)
	}
	p.config = config
	return nil
}

func (p *openAICodexProvider) NewModel(modelName, sessionID string) (contracts.InfaiModelAdaptor, error) {
	p.mu.RLock()
	config := p.config
	p.mu.RUnlock()
	adaptor, err := NewOpenAICodex(config, modelName, p.state)
	if err != nil {
		return nil, err
	}
	adaptor.(*openAICodex).sessionID = sessionID
	return adaptor, nil
}

type openAICodex struct {
	providerName string
	model        store.Model
	endpoint     string
	issuer       string
	client       *http.Client
	store        contracts.ProviderStateStore
	sessionID    string

	mu            sync.Mutex
	auth          store.Auth
	maxAttempts   int
	retryBase     time.Duration
	retryMaxDelay time.Duration
}

// NewOpenAICodex constructs a Responses adaptor from current store types.
func NewOpenAICodex(provider store.Provider, modelName string, providerStore contracts.ProviderStateStore) (contracts.InfaiModelAdaptor, error) {
	model, ok := provider.Model(modelName)
	if !ok {
		return nil, codexError("model %q is not configured", modelName)
	}
	if provider.ID == "" || providerStore == nil {
		return nil, codexError("provider ID and store are required")
	}
	if provider.Auth.Type != store.AuthTypeOAuth || provider.Auth.RefreshToken == "" {
		return nil, codexError("provider requires OAuth authentication")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return newOpenAICodex(provider, model, providerStore, &http.Client{Transport: transport}, defaultOpenAICodexURLs), nil
}

func newOpenAICodex(provider store.Provider, model store.Model, providerStore contracts.ProviderStateStore, client *http.Client, urls openAICodexURLs) *openAICodex {
	endpoint := provider.BaseEndpoint
	if endpoint == "" {
		endpoint = urls.endpoint
	}
	return &openAICodex{
		providerName: provider.ID, model: model, endpoint: normalizeCodexEndpoint(endpoint), issuer: urls.issuer,
		client: client, store: providerStore, auth: provider.Auth, maxAttempts: 4,
		retryBase: 500 * time.Millisecond, retryMaxDelay: 10 * time.Second,
	}
}

func normalizeCodexEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(endpoint, "/codex/responses") {
		return endpoint
	}
	for _, suffix := range []string{"/v1/responses", "/responses", "/chat/completions"} {
		endpoint = strings.TrimSuffix(endpoint, suffix)
	}
	return strings.TrimRight(endpoint, "/") + "/codex/responses"
}

type codexSessionKey struct{}

// WithOpenAICodexSessionID adds optional Codex request-correlation headers.
func WithOpenAICodexSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, codexSessionKey{}, strings.TrimSpace(sessionID))
}

type codexResponseRequest struct {
	Model             string              `json:"model"`
	Store             bool                `json:"store"`
	Stream            bool                `json:"stream"`
	Instructions      string              `json:"instructions"`
	Input             []any               `json:"input"`
	Text              map[string]string   `json:"text"`
	Include           []string            `json:"include"`
	PromptCacheKey    string              `json:"prompt_cache_key,omitempty"`
	ToolChoice        string              `json:"tool_choice"`
	ParallelToolCalls bool                `json:"parallel_tool_calls"`
	Tools             []codexResponseTool `json:"tools,omitempty"`
	Reasoning         *codexReasoning     `json:"reasoning,omitempty"`
	Temperature       float64             `json:"temperature,omitempty"`
}

type codexResponseTool struct {
	Type        string                   `json:"type"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Parameters  contracts.ToolParameters `json:"parameters"`
}

type codexReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

func (o *openAICodex) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	body, err := o.buildRequest(messages, tools, opts)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	resp, err := o.send(ctx, body, opts)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	defer resp.Body.Close()
	return readCodexStream(ctx, resp.Body, opts)
}

func (o *openAICodex) buildRequest(messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) ([]byte, error) {
	instructions := make([]string, 0)
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		text := message.Text()
		switch message.Role {
		case "system":
			if text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			input = append(input, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
		case "assistant":
			if text != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": text})
		}
	}
	if len(instructions) == 0 {
		instructions = append(instructions, "You are a helpful assistant.")
	}
	req := codexResponseRequest{
		Model: o.model.Name, Store: false, Stream: true, Instructions: strings.Join(instructions, "\n\n"), Input: input,
		Text: map[string]string{"verbosity": "low"}, Include: []string{"reasoning.encrypted_content"},
		PromptCacheKey: o.sessionID, ToolChoice: "auto", ParallelToolCalls: true,
	}
	for _, tool := range tools {
		req.Tools = append(req.Tools, codexResponseTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	effort := o.model.ReasoningEffort
	if opts != nil {
		if opts.ReasoningEffort != "" {
			effort = opts.ReasoningEffort
		}
		if opts.Temperature != 0 {
			req.Temperature = opts.Temperature
		}
	}
	if effort != "" && effort != "off" {
		if effort == "minimal" {
			effort = "low"
		}
		req.Reasoning = &codexReasoning{Effort: effort, Summary: "auto"}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, codexError("encode request: %v", err)
	}
	return raw, nil
}

func (o *openAICodex) currentAuth(ctx context.Context, force bool, staleAccess string) (store.Auth, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Another request may already have refreshed while this one waited for the lock.
	if force && staleAccess != "" && o.auth.AccessToken != staleAccess {
		return o.auth, nil
	}
	if !force && o.auth.AccessToken != "" && time.Until(o.auth.ExpiresAt) > 5*time.Minute {
		return o.auth, nil
	}
	auth, err := refreshCodexToken(ctx, o.client, o.issuer, o.auth.RefreshToken, o.auth.AccountID)
	if err != nil {
		return store.Auth{}, err
	}
	provider, ok := o.store.Get(o.providerName)
	if !ok {
		return store.Auth{}, codexError("persist refreshed authentication: provider %q no longer exists", o.providerName)
	}
	provider.Auth = auth
	if err := o.store.UpdateProvider(provider); err != nil {
		return store.Auth{}, codexError("persist refreshed authentication: %v", err)
	}
	o.auth = auth
	return auth, nil
}

func (o *openAICodex) send(ctx context.Context, body []byte, opts *contracts.GenerateOptions) (*http.Response, error) {
	forceRefresh := false
	staleAccess := ""
	for attempt := 1; attempt <= o.maxAttempts; attempt++ {
		auth, err := o.currentAuth(ctx, forceRefresh, staleAccess)
		if err != nil {
			return nil, err
		}
		forceRefresh = false
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, codexError("create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
		req.Header.Set("chatgpt-account-id", auth.AccountID)
		setCodexClientHeaders(req.Header)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		sessionID, _ := ctx.Value(codexSessionKey{}).(string)
		if sessionID == "" {
			sessionID = o.sessionID
		}
		if sessionID != "" {
			req.Header.Set("session-id", sessionID)
			req.Header.Set("thread-id", sessionID)
			req.Header.Set("x-client-request-id", sessionID)
		}
		resp, err := o.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, codexWrap("request", ctx.Err())
			}
			if attempt == o.maxAttempts || !isRetryableTransportError(err) {
				return nil, codexError("request failed after %d attempt(s): %v", attempt, err)
			}
			if err := o.waitRetry(ctx, attempt, 0, opts); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt < o.maxAttempts {
			staleAccess = auth.AccessToken
			forceRefresh = true
			continue
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
		if resp.StatusCode == http.StatusTooManyRequests && terminalQuotaError(errorBody) {
			retryable = false
		}
		statusErr := codexError("status %d: %s", resp.StatusCode, sanitizeBody(errorBody))
		if !retryable || attempt == o.maxAttempts {
			return nil, statusErr
		}
		if err := o.waitRetry(ctx, attempt, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()), opts); err != nil {
			return nil, err
		}
	}
	return nil, codexError("retry loop exhausted")
}

func (o *openAICodex) waitRetry(ctx context.Context, attempt int, retryAfter time.Duration, opts *contracts.GenerateOptions) error {
	delay := o.retryBase
	for i := 1; i < attempt && delay < o.retryMaxDelay; i++ {
		delay *= 2
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > o.retryMaxDelay {
		delay = o.retryMaxDelay
	}
	if opts != nil && opts.OnDelta != nil {
		opts.OnDelta(contracts.DeltaStatus, fmt.Sprintf("Codex unavailable; retrying in %s", delay))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return codexWrap("retry", ctx.Err())
	case <-timer.C:
		return nil
	}
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type codexStreamEvent struct {
	Type        string `json:"type"`
	Delta       string `json:"delta"`
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Arguments   string `json:"arguments"`
	Item        struct {
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response struct {
		Usage             *codexUsage `json:"usage"`
		Status            string      `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error json.RawMessage `json:"error"`
	} `json:"response"`
	Usage   *codexUsage     `json:"usage"`
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
}

func readCodexStream(ctx context.Context, body io.Reader, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	dec := NewDecoder(body)
	var content, reasoning strings.Builder
	var usage *contracts.TokenUsage
	toolCalls := make([]contracts.ToolCall, 0)
	toolIndex := make(map[string]int)
	terminal := false
	for {
		if err := ctx.Err(); err != nil {
			return contracts.ChatMessage{}, nil, codexError("stream: %v", err)
		}
		event, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return contracts.ChatMessage{}, nil, codexError("read stream: %v", err)
		}
		if event.Data == "[DONE]" {
			terminal = true
			break
		}
		var value codexStreamEvent
		if err := json.Unmarshal([]byte(event.Data), &value); err != nil {
			return contracts.ChatMessage{}, nil, codexError("malformed stream event %q: %v", event.Event, err)
		}
		kind := value.Type
		if kind == "" {
			kind = event.Event
		}
		switch kind {
		case "response.output_text.delta":
			content.WriteString(value.Delta)
			emitCodexDelta(opts, contracts.DeltaContent, value.Delta)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(value.Delta)
			emitCodexDelta(opts, contracts.DeltaReasoning, value.Delta)
		case "response.output_item.added":
			if value.Item.Type == "function_call" {
				id := value.Item.CallID
				if id == "" {
					id = value.Item.ID
				}
				toolIndex[value.Item.ID] = len(toolCalls)
				toolIndex[strconv.Itoa(value.OutputIndex)] = len(toolCalls)
				toolCalls = append(toolCalls, contracts.ToolCall{ID: id, Type: "function", Function: contracts.Function{Name: value.Item.Name, Arguments: value.Item.Arguments}})
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			index, ok := toolIndex[value.ItemID]
			if !ok {
				index, ok = toolIndex[strconv.Itoa(value.OutputIndex)]
			}
			if !ok {
				return contracts.ChatMessage{}, nil, codexError("function arguments reference unknown item %q", value.ItemID)
			}
			if kind == "response.function_call_arguments.done" && value.Arguments != "" {
				toolCalls[index].Function.Arguments = value.Arguments
			} else {
				toolCalls[index].Function.Arguments += value.Delta
			}
		case "response.completed", "response.done":
			terminal = true
			usage = mapCodexUsage(value.Response.Usage)
			if usage == nil {
				usage = mapCodexUsage(value.Usage)
			}
		case "response.incomplete":
			return contracts.ChatMessage{}, mapCodexUsage(value.Response.Usage), codexError("response incomplete: %s", value.Response.IncompleteDetails.Reason)
		case "response.failed":
			return contracts.ChatMessage{}, nil, codexError("response failed: %s", streamError(value.Response.Error, value.Message))
		case "error":
			return contracts.ChatMessage{}, nil, codexError("stream error: %s", streamError(value.Error, value.Message))
		}
	}
	if !terminal {
		return contracts.ChatMessage{}, usage, codexError("stream ended before a terminal event")
	}
	text := content.String()
	return contracts.ChatMessage{Role: "assistant", Content: &text, ReasoningContent: reasoning.String(), ToolCalls: toolCalls}, usage, nil
}

func emitCodexDelta(opts *contracts.GenerateOptions, kind contracts.DeltaKind, text string) {
	if text != "" && opts != nil && opts.OnDelta != nil {
		opts.OnDelta(kind, text)
	}
}

func mapCodexUsage(usage *codexUsage) *contracts.TokenUsage {
	if usage == nil {
		return nil
	}
	return &contracts.TokenUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
}

func streamError(raw json.RawMessage, fallback string) string {
	if len(raw) > 0 && string(raw) != "null" {
		var value struct{ Message, Code string }
		if json.Unmarshal(raw, &value) == nil {
			if value.Message != "" {
				return value.Message
			}
			if value.Code != "" {
				return value.Code
			}
		}
		return sanitizeBody(raw)
	}
	if fallback != "" {
		return fallback
	}
	return "unknown error"
}

func responseErrorCode(body []byte) string {
	var value struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &value) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(value.Error, &code) == nil {
		return code
	}
	var nested struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(value.Error, &nested)
	return nested.Code
}

func sanitizeBody(body []byte) string {
	if len(body) > 4096 {
		body = body[:4096]
	}
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			return r
		}
		return ' '
	}, string(body))
	return strings.TrimSpace(s)
}

func terminalQuotaError(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, phrase := range []string{"insufficient_quota", "quota exceeded", "usage limit", "billing hard limit", "not enough credits"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func codexError(format string, args ...any) error {
	return fmt.Errorf("openai codex: "+format, args...)
}

func codexWrap(operation string, err error) error {
	return fmt.Errorf("openai codex: %s: %w", operation, err)
}

func clientVersion() string {
	version := strings.TrimSpace(internalconfig.Version())
	if version == "" || version == "dev" {
		return "0.0.0"
	}
	return strings.TrimPrefix(version, "v")
}

func setCodexClientHeaders(headers http.Header) {
	version := clientVersion()
	headers.Set("originator", "infai")
	headers.Set("version", version)
	headers.Set("User-Agent", fmt.Sprintf("infai/%s (%s; %s)", version, runtime.GOOS, runtime.GOARCH))
}

var _ contracts.InfaiModelAdaptor = (*openAICodex)(nil)
