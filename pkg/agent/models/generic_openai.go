package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type genericOpenAICompatableAPI struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

func NewOpenAICompatableAPI(baseURL, model, apiKey string) *genericOpenAICompatableAPI {
	return &genericOpenAICompatableAPI{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 5 * time.Minute},
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

func (o *genericOpenAICompatableAPI) Generate(ctx context.Context, messages []contracts.ChatMessage, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	reqBody := openAIChatRequest{
		Model:    o.model,
		Messages: messages,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return contracts.ChatMessage{}, nil, fmt.Errorf("openai compatible api: status %d: %s", resp.StatusCode, string(body))
	}

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

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
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
	return msg, usage, nil
}
