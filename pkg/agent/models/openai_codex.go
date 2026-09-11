package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var codexOAuthEndpoints = struct {
	deviceStart string
	deviceToken string
	token       string
	verify      string
}{
	deviceStart: "https://auth.openai.com/api/accounts/deviceauth/usercode",
	deviceToken: "https://auth.openai.com/api/accounts/deviceauth/token",
	token:       "https://auth.openai.com/oauth/token",
	verify:      "https://auth.openai.com/codex/device",
}

func codexAuthMethods() []contracts.ProviderAuthMethod {
	return []contracts.ProviderAuthMethod{{
		Method:      contracts.OAuth2,
		Name:        "OpenAI account",
		Description: "Authorize with a device code in your browser",
	}}
}

func beginCodexAuth(ctx context.Context, method contracts.LLMProviderAuthMethod) (contracts.ProviderAuthFlow, error) {
	if method != contracts.OAuth2 {
		return nil, fmt.Errorf("openai codex does not support auth method %q", method)
	}
	return beginCodexDeviceAuth(ctx)
}

func refreshCodexAuth(ctx context.Context, auth contracts.LLMProviderAuth) (contracts.LLMProviderAuth, bool, error) {
	if auth.Method != contracts.OAuth2 {
		return auth, false, errors.New("openai codex auth: OAuth credentials are missing; log in again")
	}
	if auth.ExpiresAt == nil {
		return auth, false, errors.New("openai codex auth: token expiry is missing; log in again")
	}
	if auth.ExpiresAt.After(time.Now().Add(5 * time.Minute)) {
		return auth, false, nil
	}
	if strings.TrimSpace(auth.RefreshToken) == "" {
		return auth, false, errors.New("openai codex auth: refresh token is missing; log in again")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {auth.RefreshToken},
		"client_id":     {codexOAuthClientID},
	}
	refreshed, err := exchangeCodexToken(ctx, values)
	if err != nil {
		return auth, false, fmt.Errorf("openai codex auth: refresh token: %w", err)
	}
	return refreshed, true, nil
}

type codexDeviceAuthFlow struct {
	client       *http.Client
	deviceAuthID string
	userCode     string
	interval     time.Duration
}

func beginCodexDeviceAuth(ctx context.Context) (*codexDeviceAuthFlow, error) {
	body, err := json.Marshal(map[string]string{"client_id": codexOAuthClientID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthEndpoints.deviceStart, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai codex auth: request device code: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, providerHTTPError("openai codex auth: request device code", resp)
	}
	var result struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     flexibleSeconds `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai codex auth: decode device code: %w", err)
	}
	if result.DeviceAuthID == "" || result.UserCode == "" {
		return nil, errors.New("openai codex auth: device response is incomplete")
	}
	if result.Interval < 1 {
		result.Interval = 1
	}
	return &codexDeviceAuthFlow{
		client:       client,
		deviceAuthID: result.DeviceAuthID,
		userCode:     result.UserCode,
		interval:     time.Duration(result.Interval) * time.Second,
	}, nil
}

func (f *codexDeviceAuthFlow) Challenge() contracts.ProviderAuthChallenge {
	return contracts.ProviderAuthChallenge{VerificationURL: codexOAuthEndpoints.verify, UserCode: f.userCode}
}

func (f *codexDeviceAuthFlow) Complete(ctx context.Context) (contracts.LLMProviderAuth, error) {
	interval := f.interval
	for {
		code, verifier, pending, slowDown, err := f.poll(ctx)
		if err != nil {
			return contracts.LLMProviderAuth{}, err
		}
		if code != "" && verifier != "" {
			values := url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {codexOAuthClientID},
				"code":          {code},
				"code_verifier": {verifier},
				"redirect_uri":  {"https://auth.openai.com/deviceauth/callback"},
			}
			auth, err := exchangeCodexToken(ctx, values)
			if err != nil {
				return contracts.LLMProviderAuth{}, fmt.Errorf("openai codex auth: exchange device code: %w", err)
			}
			return auth, nil
		}
		if !pending {
			return contracts.LLMProviderAuth{}, errors.New("openai codex auth: device authorization returned no code")
		}
		if slowDown {
			interval += 5 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return contracts.LLMProviderAuth{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (f *codexDeviceAuthFlow) poll(ctx context.Context) (code, verifier string, pending, slowDown bool, err error) {
	body, err := json.Marshal(map[string]string{"device_auth_id": f.deviceAuthID, "user_code": f.userCode})
	if err != nil {
		return "", "", false, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthEndpoints.deviceToken, bytes.NewReader(body))
	if err != nil {
		return "", "", false, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", "", false, false, fmt.Errorf("openai codex auth: poll device authorization: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return "", "", true, false, nil
	}
	var result struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
		Error             string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		return "", "", false, false, fmt.Errorf("openai codex auth: decode device authorization: %w", err)
	}
	switch result.Error {
	case "deviceauth_authorization_pending":
		return "", "", true, false, nil
	case "slow_down":
		return "", "", true, true, nil
	}
	if resp.StatusCode != http.StatusOK || result.Error != "" {
		if result.Error == "" {
			result.Error = resp.Status
		}
		return "", "", false, false, fmt.Errorf("openai codex auth: device authorization: %s", result.Error)
	}
	return result.AuthorizationCode, result.CodeVerifier, false, false, nil
}

func exchangeCodexToken(ctx context.Context, values url.Values) (contracts.LLMProviderAuth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthEndpoints.token, strings.NewReader(values.Encode()))
	if err != nil {
		return contracts.LLMProviderAuth{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return contracts.LLMProviderAuth{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return contracts.LLMProviderAuth{}, providerHTTPError("token endpoint", resp)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return contracts.LLMProviderAuth{}, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return contracts.LLMProviderAuth{}, errors.New("token response is incomplete")
	}
	accountID, err := codexAccountID(token.AccessToken)
	if err != nil {
		return contracts.LLMProviderAuth{}, err
	}
	expires := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return contracts.LLMProviderAuth{
		Method: contracts.OAuth2, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ExpiresAt: &expires, AccountID: accountID,
	}, nil
}

func codexAccountID(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("access token has an invalid JWT payload")
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("access token has an invalid JWT payload")
	}
	var authClaims struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(claims["https://api.openai.com/auth"], &authClaims); err != nil || authClaims.AccountID == "" {
		return "", errors.New("access token does not contain a ChatGPT account ID")
	}
	return authClaims.AccountID, nil
}

func providerHTTPError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("%s: status %d: %s", action, resp.StatusCode, message)
}

type flexibleSeconds int

func (s *flexibleSeconds) UnmarshalJSON(data []byte) error {
	value := strings.Trim(string(data), `"`)
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return fmt.Errorf("invalid interval %q", value)
	}
	*s = flexibleSeconds(seconds)
	return nil
}

type openAICodexResponsesAPI struct {
	b        contracts.ProvisionedModel
	endpoint *url.URL
	client   *http.Client
}

func NewOpenAICodexResponsesAPI(b contracts.ProvisionedModel) (*openAICodexResponsesAPI, error) {
	base, err := url.Parse(b.BaseEndpoint())
	if err != nil || !base.IsAbs() || base.Host == "" {
		return nil, fmt.Errorf("openai codex responses api: invalid base endpoint %q", b.BaseEndpoint())
	}
	path := strings.TrimSuffix(base.Path, "/")
	switch {
	case strings.HasSuffix(path, "/codex/responses"):
	case strings.HasSuffix(path, "/codex"):
		base = base.JoinPath("responses")
	default:
		base = base.JoinPath("codex", "responses")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return &openAICodexResponsesAPI{
		b:        b,
		endpoint: base,
		client:   &http.Client{Transport: transport},
	}, nil
}

func (o *openAICodexResponsesAPI) GetModelSpecs() contracts.ProvisionedModel { return o.b }

type codexResponsesRequest struct {
	Model             string            `json:"model"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Instructions      string            `json:"instructions"`
	Input             []any             `json:"input"`
	Text              map[string]string `json:"text"`
	Include           []string          `json:"include"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
	Tools             []codexTool       `json:"tools,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	Reasoning         *codexReasoning   `json:"reasoning,omitempty"`
}

type codexTool struct {
	Type        string                   `json:"type"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Parameters  contracts.ToolParameters `json:"parameters"`
}

type codexReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

func (o *openAICodexResponsesAPI) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	instructions, input, err := codexInput(messages)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	if instructions == "" {
		return contracts.ChatMessage{}, nil, fmt.Errorf("instructions recieved by the openai-codex provider is empty")
	}

	body := codexResponsesRequest{
		Model:             o.b.Model().Id,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             input,
		Text:              map[string]string{"verbosity": "low"},
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Temperature:       o.b.Model().DefaultTemperature,
	}
	if len(tools) > 0 {
		for _, tool := range tools {
			body.Tools = append(body.Tools, codexTool{
				Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
			})
		}
	}
	if effort, ok := o.b.ThinkingLevelValue(); ok {
		body.Reasoning = &codexReasoning{Effort: effort}
		if effort != "none" {
			body.Reasoning.Summary = "auto"
		}
	} else if o.b.Model().AvailableThinking && o.b.Model().ThinkingLevels.Off != nil {
		body.Reasoning = &codexReasoning{Effort: *o.b.Model().ThinkingLevels.Off}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	resp, err := o.send(ctx, raw)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	defer resp.Body.Close()
	return o.readStream(ctx, resp.Body, opts)
}

func codexInput(messages []contracts.ChatMessage) (string, []any, error) {
	var instructions []string
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "system", "developer":
			if text := strings.TrimSpace(message.Text()); text != "" {
				instructions = append(instructions, text)
			}
		case "tool":
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Text(),
			})
		case "assistant":
			if message.ReasoningSignature != "" {
				raw := json.RawMessage(message.ReasoningSignature)
				if !json.Valid(raw) {
					return "", nil, errors.New("openai codex responses api: invalid reasoning replay metadata")
				}
				input = append(input, raw)
			}
			if message.Content != nil {
				input = append(input, map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": message.Text()}},
				})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": call.ID,
					"name": call.Function.Name, "arguments": call.Function.Arguments,
				})
			}
		default:
			input = append(input, map[string]any{
				"type": "message", "role": message.Role,
				"content": []any{map[string]any{"type": "input_text", "text": message.Text()}},
			})
		}
	}
	return strings.Join(instructions, "\n\n"), input, nil
}

func (o *openAICodexResponsesAPI) send(ctx context.Context, body []byte) (*http.Response, error) {
	auth := o.b.Auth()
	if auth.Method != contracts.OAuth2 || strings.TrimSpace(auth.AccessToken) == "" {
		return nil, errors.New("openai codex responses api: OAuth access token is required")
	}
	if strings.TrimSpace(auth.AccountID) == "" {
		return nil, errors.New("openai codex responses api: ChatGPT account ID is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("chatgpt-account-id", auth.AccountID)
	req.Header.Set("originator", "infai")
	req.Header.Set("User-Agent", fmt.Sprintf("infaiw (%s; %s)", runtime.GOOS, runtime.GOARCH))
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("openai codex responses api: request failed: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	statusErr := fmt.Errorf("openai codex responses api: status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	if readErr != nil {
		statusErr = fmt.Errorf("%w: read response body: %v", statusErr, readErr)
	}
	return nil, statusErr
}

type codexStreamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	ItemID   string          `json:"item_id"`
	Item     json.RawMessage `json:"item"`
	Response struct {
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Usage *codexUsage `json:"usage"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type codexUsage struct {
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
	TotalTokens  uint64 `json:"total_tokens"`
}

type codexOutputItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

func (o *openAICodexResponsesAPI) readStream(ctx context.Context, body io.Reader, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	dec := NewDecoder(body)
	var content, reasoning strings.Builder
	var usage *contracts.TokenUsage
	var reasoningSignature string
	terminal := false
	hasMessage := false
	toolCalls := make([]contracts.ToolCall, 0)
	toolIndexes := make(map[string]int)

	for {
		if err := ctx.Err(); err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		event, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		if event.Data == "[DONE]" {
			break
		}
		var value codexStreamEvent
		if err := json.Unmarshal([]byte(event.Data), &value); err != nil {
			return contracts.ChatMessage{}, nil, fmt.Errorf("openai codex responses api: decode stream event: %w", err)
		}
		switch value.Type {
		case "error":
			return contracts.ChatMessage{}, nil, codexStreamError(value)
		case "response.failed":
			return contracts.ChatMessage{}, nil, codexStreamError(value)
		case "response.completed", "response.done", "response.incomplete":
			if value.Response.Error != nil {
				return contracts.ChatMessage{}, nil, codexStreamError(value)
			}
			if value.Type == "response.incomplete" || (value.Response.Status != "" && value.Response.Status != "completed") {
				return contracts.ChatMessage{}, nil, fmt.Errorf("openai codex responses api: response ended with status %q", value.Response.Status)
			}
			terminal = true
			if value.Response.Usage != nil {
				usage = &contracts.TokenUsage{
					PromptTokens: value.Response.Usage.InputTokens, CompletionTokens: value.Response.Usage.OutputTokens,
					TotalTokens: value.Response.Usage.TotalTokens,
				}
			}
		case "response.output_text.delta", "response.refusal.delta":
			hasMessage = true
			content.WriteString(value.Delta)
			emitCodexDelta(opts, contracts.DeltaContent, value.Delta)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(value.Delta)
			emitCodexDelta(opts, contracts.DeltaReasoning, value.Delta)
		case "response.reasoning_summary_part.done":
			reasoning.WriteString("\n\n")
			emitCodexDelta(opts, contracts.DeltaReasoning, "\n\n")
		case "response.output_item.added":
			item, err := decodeCodexOutputItem(value.Item)
			if err != nil {
				return contracts.ChatMessage{}, nil, err
			}
			if item.Type == "function_call" {
				key := item.ID
				if key == "" {
					key = item.CallID
				}
				toolIndexes[key] = len(toolCalls)
				toolCalls = append(toolCalls, contracts.ToolCall{
					ID: item.CallID, Type: "function",
					Function: contracts.Function{Name: item.Name, Arguments: item.Arguments},
				})
			}
			if item.Type == "message" {
				hasMessage = true
			}
		case "response.function_call_arguments.delta":
			if index, ok := toolIndexes[value.ItemID]; ok {
				toolCalls[index].Function.Arguments += value.Delta
			}
		case "response.output_item.done":
			item, err := decodeCodexOutputItem(value.Item)
			if err != nil {
				return contracts.ChatMessage{}, nil, err
			}
			if item.Type == "reasoning" && item.EncryptedContent != "" {
				reasoningSignature = string(value.Item)
			}
			if item.Type == "reasoning" && len(item.Summary) > 0 {
				reasoning.Reset()
				for _, part := range item.Summary {
					reasoning.WriteString(part.Text)
				}
			}
			if item.Type == "message" && len(item.Content) > 0 {
				hasMessage = true
				content.Reset()
				for _, part := range item.Content {
					if part.Type == "refusal" {
						content.WriteString(part.Refusal)
					} else {
						content.WriteString(part.Text)
					}
				}
			}
			if item.Type == "function_call" {
				key := item.ID
				index, ok := toolIndexes[key]
				if !ok {
					index = len(toolCalls)
					toolIndexes[key] = index
					toolCalls = append(toolCalls, contracts.ToolCall{})
				}
				toolCalls[index] = contracts.ToolCall{
					ID: item.CallID, Type: "function",
					Function: contracts.Function{Name: item.Name, Arguments: item.Arguments},
				}
			}
		}
		if terminal {
			break
		}
	}
	if !terminal {
		return contracts.ChatMessage{}, nil, errors.New("openai codex responses api: stream ended before a terminal response")
	}
	text := content.String()
	reply := contracts.ChatMessage{
		Role: "assistant", ReasoningContent: reasoning.String(), ReasoningSignature: reasoningSignature, ToolCalls: toolCalls,
	}
	if hasMessage {
		reply.Content = &text
	}
	return reply, usage, nil
}

func decodeCodexOutputItem(raw json.RawMessage) (codexOutputItem, error) {
	var item codexOutputItem
	if len(raw) == 0 {
		return item, errors.New("openai codex responses api: stream event is missing an output item")
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return item, fmt.Errorf("openai codex responses api: decode output item: %w", err)
	}
	return item, nil
}

func emitCodexDelta(opts *contracts.GenerateOptions, kind contracts.DeltaKind, text string) {
	if text != "" && opts != nil && opts.OnDelta != nil {
		opts.OnDelta(kind, text)
	}
}

func codexStreamError(event codexStreamEvent) error {
	code, message := event.Code, event.Message
	if event.Error != nil {
		code, message = event.Error.Code, event.Error.Message
	}
	if event.Response.Error != nil {
		code, message = event.Response.Error.Code, event.Response.Error.Message
	}
	if message == "" {
		message = "request failed"
	}
	if code != "" {
		return fmt.Errorf("openai codex responses api: %s: %s", code, message)
	}
	return fmt.Errorf("openai codex responses api: %s", message)
}
