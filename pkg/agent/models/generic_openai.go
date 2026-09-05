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
	"strconv"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type genericOpenAICompatableAPI struct {
	baseURL       string
	model         string
	apiKey        string
	client        *http.Client
	maxAttempts   int
	retryBase     time.Duration
	retryMaxDelay time.Duration
}

func NewOpenAICompatableAPI(baseURL, model, apiKey string) *genericOpenAICompatableAPI {
	return &genericOpenAICompatableAPI{
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         model,
		apiKey:        apiKey,
		client:        &http.Client{Timeout: 5 * time.Minute},
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
		if o.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+o.apiKey)
		}

		resp, err := o.client.Do(req)
		if err != nil {
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

		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		_ = resp.Body.Close()
		statusErr := fmt.Errorf("openai compatible api: status %d: %s", resp.StatusCode, string(responseBody))
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
