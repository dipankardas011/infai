package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

type genericOpenAICompatableAPI struct {
	baseURL         string
	model           string
	auth            store.Auth
	reasoningEffort string
	client          *http.Client
	maxAttempts     int
	retryBase       time.Duration
	retryMaxDelay   time.Duration
}

func NewOpenAICompatableAPI(baseURL, model, apiKey string) *genericOpenAICompatableAPI {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return &genericOpenAICompatableAPI{
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         model,
		auth:          store.Auth{Type: store.AuthTypeBearer, Token: apiKey},
		client:        &http.Client{Transport: transport},
		maxAttempts:   10,
		retryBase:     5 * time.Second,
		retryMaxDelay: time.Minute,
	}
}

type openAIChatRequest struct {
	Model           string                  `json:"model"`
	Messages        []contracts.ChatMessage `json:"messages"`
	MaxTokens       int                     `json:"max_tokens,omitempty"`
	Temperature     float64                 `json:"temperature,omitempty"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	Stream          bool                    `json:"stream,omitempty"`
	StreamOptions   *openAIStreamOptions    `json:"stream_options,omitempty"`
	Tools           []openAITool            `json:"tools,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function contracts.Tool `json:"function"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message contracts.ChatMessage `json:"message"`
	} `json:"choices"`
	Usage *contracts.TokenUsage `json:"usage"`
}

func (o *genericOpenAICompatableAPI) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	wireMessages := append([]contracts.ChatMessage(nil), messages...)
	for i := range wireMessages {
		wireMessages[i].Status = "" // NOTE: to avoid sending the status as openai api doesn't have one.
	}
	reqBody := openAIChatRequest{
		Model:    o.model,
		Messages: wireMessages,
	}
	for _, tool := range tools {
		reqBody.Tools = append(reqBody.Tools, openAITool{
			Type:     "function",
			Function: tool,
		})
	}
	if opts != nil {
		if opts.MaxTokens > 0 {
			reqBody.MaxTokens = opts.MaxTokens
		}
		if opts.Temperature != 0 {
			reqBody.Temperature = opts.Temperature
		}
		if opts.ReasoningEffort != "" {
			reqBody.ReasoningEffort = opts.ReasoningEffort
		}
		if opts.Stream {
			reqBody.Stream = true
			reqBody.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
		}
	}
	if reqBody.ReasoningEffort == "" {
		reqBody.ReasoningEffort = o.reasoningEffort
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}

	resp, err := o.sendChatRequest(ctx, raw, opts)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	defer resp.Body.Close()

	if opts != nil && opts.Stream {
		return o.readStream(ctx, resp.Body, opts)
	}

	var parsed openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	if len(parsed.Choices) == 0 {
		return contracts.ChatMessage{}, nil, fmt.Errorf("openai compatible api: empty choices")
	}

	reply := parsed.Choices[0].Message
	if reply.Role == "" {
		reply.Role = "assistant"
	}

	return reply, parsed.Usage, nil
}

func (o *genericOpenAICompatableAPI) sendChatRequest(ctx context.Context, body []byte, opts *contracts.GenerateOptions) (*http.Response, error) {
	maxAttempts := o.maxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		switch o.auth.Type {
		case store.AuthTypeBasic:
			req.SetBasicAuth(o.auth.Username, o.auth.Password)
		case store.AuthTypeBearer:
			if o.auth.Token != "" {
				req.Header.Set("Authorization", "Bearer "+o.auth.Token)
			}
		}

		resp, err := o.client.Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == maxAttempts || !isRetryableTransportError(err) {
				return nil, fmt.Errorf("openai compatible api: request failed after %d attempt(s): %w", attempt, err)
			}
			if err := o.waitForRetry(ctx, attempt, 0, opts); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		closeErr := resp.Body.Close()
		statusErr := fmt.Errorf("openai compatible api: status %d: %s", resp.StatusCode, string(responseBody))
		if readErr != nil {
			statusErr = fmt.Errorf("%w: read response body: %v", statusErr, readErr)
		}
		if closeErr != nil {
			statusErr = fmt.Errorf("%w: close response body: %v", statusErr, closeErr)
		}
		if attempt == maxAttempts || !isRetryableStatus(resp.StatusCode) {
			return nil, statusErr
		}
		if err := o.waitForRetry(ctx, attempt, retryAfter, opts); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("openai compatible api: retry loop exhausted")
}

func (o *genericOpenAICompatableAPI) waitForRetry(ctx context.Context, attempt int, retryAfter time.Duration, opts *contracts.GenerateOptions) error {
	delay := o.retryBase
	if delay <= 0 {
		delay = 5 * time.Second
	}
	maxDelay := o.retryMaxDelay
	if maxDelay <= 0 {
		maxDelay = time.Minute
	}
	for i := 1; i < attempt; i++ {
		if delay >= maxDelay>>1 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	if opts != nil && opts.OnDelta != nil {
		opts.OnDelta(contracts.DeltaStatus, fmt.Sprintf("LLM endpoint unavailable; retrying in %s (attempt %d/%d)", delay, attempt+1, o.maxAttempts))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isRetryableTransportError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *contracts.TokenUsage `json:"usage"`
}

// readStream consumes an OpenAI-compatible SSE chat completion stream,
// delivering text deltas (content and reasoning, in stream order) to
// opts.OnDelta and accumulating the full message and usage. It uses a
// W3C-compliant SSE decoder so multi-line data, comments, and CRLF all work
// across modern providers.
func (o *genericOpenAICompatableAPI) readStream(ctx context.Context, body io.Reader, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	dec := NewDecoder(body)

	var content, reasoning strings.Builder
	var usage *contracts.TokenUsage
	var toolCalls []contracts.ToolCall

	for {
		if err := ctx.Err(); err != nil {
			return contracts.ChatMessage{}, nil, err
		}

		ev, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		if ev.Data == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		if len(chunk.Choices) > 0 {
			d := chunk.Choices[0].Delta
			if d.Content != "" {
				content.WriteString(d.Content)
				if opts.OnDelta != nil {
					opts.OnDelta(contracts.DeltaContent, d.Content)
				}
			}
			if d.ReasoningContent != "" {
				reasoning.WriteString(d.ReasoningContent)
				if opts.OnDelta != nil {
					opts.OnDelta(contracts.DeltaReasoning, d.ReasoningContent)
				}
			}
			for _, delta := range d.ToolCalls {
				if delta.Index < 0 {
					return contracts.ChatMessage{}, nil, fmt.Errorf("openai compatible api: invalid tool call index %d", delta.Index)
				}
				for len(toolCalls) <= delta.Index {
					toolCalls = append(toolCalls, contracts.ToolCall{})
				}
				call := &toolCalls[delta.Index]
				if call.ID == "" {
					call.ID = delta.ID
				}
				if call.Type == "" {
					call.Type = delta.Type
				}
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
			}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	text := content.String()
	msg := contracts.ChatMessage{
		Role:             "assistant",
		Content:          &text,
		ReasoningContent: reasoning.String(),
	}
	for _, call := range toolCalls {
		if call.ID != "" || call.Function.Name != "" || call.Function.Arguments != "" {
			msg.ToolCalls = append(msg.ToolCalls, call)
		}
	}
	return msg, usage, nil
}

type genericOpenAIProvider struct {
	mu           sync.RWMutex
	config       store.Provider
	baseEndpoint string
	state        contracts.ProviderStateStore
	client       *http.Client
}

func newGenericOpenAIProvider(config store.Provider, state contracts.ProviderStateStore) (contracts.LLMProvider, error) {
	if state == nil {
		return nil, errors.New("generic openai: provider state store is required")
	}
	return &genericOpenAIProvider{
		config: config, baseEndpoint: config.BaseEndpoint, state: state,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *genericOpenAIProvider) ID() string { return p.config.ID }
func (p *genericOpenAIProvider) Config() contracts.ProviderConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProviderConfig(p.config)
}

func (p *genericOpenAIProvider) Login(ctx context.Context, request contracts.ProviderLoginRequest) error {
	var auth store.Auth
	switch request.Method {
	case store.AuthTypeNone:
		auth.Type = store.AuthTypeNone
	case store.AuthTypeBasic:
		if request.Username == "" && request.Password == "" {
			return errors.New("generic openai: username or password is required for basic authentication")
		}
		auth = store.Auth{Type: store.AuthTypeBasic, Username: request.Username, Password: request.Password}
	case store.AuthTypeBearer:
		if request.Token == "" {
			return errors.New("generic openai: bearer token is required")
		}
		auth = store.Auth{Type: store.AuthTypeBearer, Token: request.Token}
	default:
		return fmt.Errorf("generic openai: unsupported authentication type %q", request.Method)
	}
	p.mu.Lock()
	config := p.config
	config.Auth = auth
	if err := p.state.UpdateProvider(config); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("generic openai: persist authentication: %w", err)
	}
	p.config = config
	p.mu.Unlock()
	if err := p.RefreshModels(ctx); err != nil {
		return fmt.Errorf("generic openai: authentication saved but model refresh failed: %w", err)
	}
	return nil
}

func (p *genericOpenAIProvider) Logout() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	config := p.config
	config.Auth = store.Auth{}
	if err := p.state.UpdateProvider(config); err != nil {
		return fmt.Errorf("generic openai: persist logout: %w", err)
	}
	p.config = config
	return nil
}

func (p *genericOpenAIProvider) RefreshModels(ctx context.Context) error {
	p.mu.RLock()
	config := p.config
	p.mu.RUnlock()
	baseEndpoint, err := resolveGenericBaseEndpoint(ctx, p.client, config, providerMetadataURL)
	if err != nil {
		return err
	}
	discoveryConfig := config
	discoveryConfig.BaseEndpoint = baseEndpoint
	models, err := discoverGenericOpenAIModels(ctx, p.client, discoveryConfig, providerMetadataURL)
	if err != nil {
		return err
	}
	config.Models = models
	if err := p.state.UpdateProvider(config); err != nil {
		return fmt.Errorf("generic openai: persist model catalog: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = config
	p.baseEndpoint = baseEndpoint
	return nil
}

func (p *genericOpenAIProvider) NewModel(modelName, _ string) (contracts.InfaiModelAdaptor, error) {
	p.mu.RLock()
	model, ok := p.config.Model(modelName)
	config := p.config
	baseEndpoint := p.baseEndpoint
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("generic openai: model %q is not configured", modelName)
	}
	if baseEndpoint == "" {
		var err error
		baseEndpoint, err = resolveGenericBaseEndpoint(context.Background(), p.client, config, providerMetadataURL)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.baseEndpoint = baseEndpoint
		p.mu.Unlock()
	}
	adaptor := NewOpenAICompatableAPI(baseEndpoint, model.Name, "")
	adaptor.auth = config.Auth
	adaptor.reasoningEffort = model.ReasoningEffort
	if mapped, exists := model.ThinkingLevelMap[model.ReasoningEffort]; exists {
		adaptor.reasoningEffort = mapped
	}
	return adaptor, nil
}

type genericModelsResponse struct {
	Data []struct {
		ID   string `json:"id"`
		Meta struct {
			ContextWindow int `json:"n_ctx"`
		} `json:"meta"`
	} `json:"data"`
	Models []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	} `json:"models"`
}

func discoverGenericOpenAIModels(ctx context.Context, client *http.Client, config store.Provider, metadataURL string) (map[string]store.Model, error) {
	var metadata providerMetadata
	metadataCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if document, metadataErr := fetchProviderMetadata(metadataCtx, client, metadataURL); metadataErr == nil {
		metadata, _ = document.provider(config.ID, config.BaseEndpoint)
	}
	cancel()
	if metadata.API != "" && metadata.API != "openai-completions" {
		return nil, fmt.Errorf("generic openai: provider %q uses unsupported API %q", config.ID, metadata.API)
	}

	baseEndpoint := config.BaseEndpoint
	if baseEndpoint == "" {
		baseEndpoint = metadata.BaseEndpoint
	}
	if baseEndpoint == "" {
		return nil, fmt.Errorf("generic openai: provider %q has no catalog base_endpoint or custom override", config.ID)
	}
	endpoint := strings.TrimRight(baseEndpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions")
	endpoint = strings.TrimSuffix(endpoint, "/responses") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("generic openai: create models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	applyGenericAuth(req, config.Auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generic openai: list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("generic openai: list models status %d: %s", resp.StatusCode, sanitizeBody(body))
	}
	var response genericModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&response); err != nil {
		return nil, fmt.Errorf("generic openai: decode models response: %w", err)
	}
	live := make(map[string]int)
	for _, model := range response.Models {
		id := model.Model
		if id == "" {
			id = model.Name
		}
		if id != "" {
			live[id] = 0
		}
	}
	for _, model := range response.Data {
		if model.ID != "" {
			live[model.ID] = model.Meta.ContextWindow
		}
	}
	if len(live) == 0 {
		return nil, errors.New("generic openai: models response contains no model IDs")
	}

	models := make(map[string]store.Model, len(live))
	var missing []string
	for id, liveContext := range live {
		model, _ := config.Model(id)
		if catalogModel, exists := metadata.model(id); exists {
			model.Name = catalogModel.Name
			model.DisplayName = catalogModel.DisplayName
			model.ContextWindow = catalogModel.ContextWindow
			model.Input = catalogModel.Input
			model.Reasoning = catalogModel.Reasoning
			model.ThinkingModes = catalogModel.ThinkingModes
			model.ThinkingLevelMap = catalogModel.ThinkingLevelMap
		}
		model.Name = id
		if liveContext > 0 {
			model.ContextWindow = liveContext
		}
		if model.ContextWindow <= 0 {
			missing = append(missing, id)
			continue
		}
		models[id] = model
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("generic openai: context length unavailable for %s; add metadata to %s or configure the models manually", strings.Join(missing, ", "), metadataURL)
	}
	return models, nil
}

func resolveGenericBaseEndpoint(ctx context.Context, client *http.Client, config store.Provider, metadataURL string) (string, error) {
	if config.BaseEndpoint != "" {
		return config.BaseEndpoint, nil
	}
	metadataCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	document, err := fetchProviderMetadata(metadataCtx, client, metadataURL)
	if err != nil {
		return "", fmt.Errorf("generic openai: resolve provider %q: %w", config.ID, err)
	}
	metadata, ok := document.provider(config.ID, "")
	if !ok || metadata.BaseEndpoint == "" {
		return "", fmt.Errorf("generic openai: provider %q is absent from the provider catalog", config.ID)
	}
	if metadata.API != "openai-completions" {
		return "", fmt.Errorf("generic openai: provider %q uses unsupported API %q", config.ID, metadata.API)
	}
	return metadata.BaseEndpoint, nil
}

func applyGenericAuth(req *http.Request, auth store.Auth) {
	switch auth.Type {
	case store.AuthTypeBasic:
		req.SetBasicAuth(auth.Username, auth.Password)
	case store.AuthTypeBearer:
		if auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+auth.Token)
		}
	}
}
