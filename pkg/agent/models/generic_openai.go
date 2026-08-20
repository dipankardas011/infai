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
