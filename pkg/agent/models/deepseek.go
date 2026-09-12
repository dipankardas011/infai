package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func deepSeekAuthMethods() []contracts.ProviderAuthMethod {
	return []contracts.ProviderAuthMethod{{
		Method:      contracts.APIKey,
		Name:        "API key",
		Description: "Authenticate with a DeepSeek API key",
		SecretInput: true,
	}}
}

func beginDeepSeekAuth(method contracts.LLMProviderAuthMethod, credential string) (contracts.ProviderAuthFlow, error) {
	if method != contracts.APIKey {
		return nil, fmt.Errorf("deepseek does not support auth method %q", method)
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return nil, errors.New("API key is required")
	}
	return staticAuthFlow{auth: contracts.LLMProviderAuth{Method: contracts.APIKey, BearerToken: credential}}, nil
}

type staticAuthFlow struct {
	auth contracts.LLMProviderAuth
}

func (staticAuthFlow) Challenge() contracts.ProviderAuthChallenge {
	return contracts.ProviderAuthChallenge{}
}

func (f staticAuthFlow) Complete(context.Context) (contracts.LLMProviderAuth, error) {
	return f.auth, nil
}

type deepSeekAPI struct {
	b         contracts.ProvisionedModel
	transport *genericOpenAICompatableAPI
}

func NewDeepSeekAPI(model contracts.ProvisionedModel) (*deepSeekAPI, error) {
	api, err := NewOpenAICompatableAPI(model)
	if err != nil {
		return nil, err
	}
	return &deepSeekAPI{b: model, transport: api}, nil
}

func (d *deepSeekAPI) GetModelSpecs() contracts.ProvisionedModel { return d.b }

type deepSeekThinking struct {
	Type string `json:"type"`
}

type deepSeekChatRequest struct {
	Model           string                  `json:"model"`
	Messages        []contracts.ChatMessage `json:"messages"`
	MaxTokens       uint64                  `json:"max_tokens,omitempty"`
	Temperature     *float64                `json:"temperature,omitempty"`
	Thinking        *deepSeekThinking       `json:"thinking,omitempty"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	Stream          bool                    `json:"stream,omitempty"`
	StreamOptions   *deepSeekStreamOptions  `json:"stream_options,omitempty"`
	Tools           []deepSeekTool          `json:"tools,omitempty"`
}

type deepSeekStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type deepSeekTool struct {
	Type     string               `json:"type"`
	Function deepSeekToolFunction `json:"function"`
}

type deepSeekToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  deepSeekToolParameters `json:"parameters"`
}

type deepSeekToolParameters struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

type deepSeekChatResponse struct {
	Choices []struct {
		Message      contracts.ChatMessage `json:"message"`
		FinishReason string                `json:"finish_reason"`
	} `json:"choices"`
	Usage *contracts.TokenUsage `json:"usage"`
}

func (d *deepSeekAPI) Generate(ctx context.Context, messages []contracts.ChatMessage, tools []contracts.Tool, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	wireMessages := append([]contracts.ChatMessage(nil), messages...)
	for i := range wireMessages {
		wireMessages[i].Status = ""
		wireMessages[i].ReasoningSignature = ""
		if wireMessages[i].Role == "assistant" && wireMessages[i].Content == nil {
			empty := ""
			wireMessages[i].Content = &empty
		}
	}

	reqBody := deepSeekChatRequest{
		Model:       d.b.Model().Id,
		Messages:    wireMessages,
		MaxTokens:   d.b.Model().MaxOutputTokens,
		Temperature: d.b.Model().DefaultTemperature,
	}

	switch d.b.ThinkingPattern() {
	case "":
		// DeepSeek defaults to thinking enabled with high effort.
		reqBody.Temperature = nil
	case contracts.ThinkingOff:
		reqBody.Thinking = &deepSeekThinking{Type: "disabled"}
	default:
		effort, ok := d.b.ThinkingLevelValue()
		if !ok {
			return contracts.ChatMessage{}, nil, fmt.Errorf("deepseek: thinking pattern %q has no configured value", d.b.ThinkingPattern())
		}
		effort, err := normalizeDeepSeekReasoningEffort(effort)
		if err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		reqBody.Thinking = &deepSeekThinking{Type: "enabled"}
		reqBody.ReasoningEffort = effort
		reqBody.Temperature = nil
	}

	for _, tool := range tools {
		reqBody.Tools = append(reqBody.Tools, deepSeekTool{
			Type: "function",
			Function: deepSeekToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters: deepSeekToolParameters{
					Type:                 tool.Parameters.Type,
					Properties:           tool.Parameters.Properties,
					Required:             append([]string{}, tool.Parameters.RequiredFields...),
					AdditionalProperties: tool.Parameters.AdditionalProperties,
				},
			},
		})
	}
	if opts != nil && opts.Stream {
		reqBody.Stream = true
		reqBody.StreamOptions = &deepSeekStreamOptions{IncludeUsage: true}
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	resp, err := d.transport.sendChatRequest(ctx, raw, opts)
	if err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	defer resp.Body.Close()

	if opts != nil && opts.Stream {
		return d.readStream(ctx, resp.Body, opts)
	}

	var parsed deepSeekChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return contracts.ChatMessage{}, nil, err
	}
	if len(parsed.Choices) == 0 {
		return contracts.ChatMessage{}, parsed.Usage, errors.New("deepseek: empty choices")
	}
	if err := validateDeepSeekFinishReason(parsed.Choices[0].FinishReason); err != nil {
		return contracts.ChatMessage{}, parsed.Usage, err
	}
	reply := parsed.Choices[0].Message
	if reply.Role == "" {
		reply.Role = "assistant"
	}
	return reply, parsed.Usage, nil
}

type deepSeekStreamChunk struct {
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
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *contracts.TokenUsage `json:"usage"`
}

func (d *deepSeekAPI) readStream(ctx context.Context, body io.Reader, opts *contracts.GenerateOptions) (contracts.ChatMessage, *contracts.TokenUsage, error) {
	dec := NewDecoder(body)
	var content, reasoning strings.Builder
	var usage *contracts.TokenUsage
	var toolCalls []contracts.ToolCall
	var finishReason string

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

		var chunk deepSeekStreamChunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return contracts.ChatMessage{}, nil, err
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if opts.OnDelta != nil {
					opts.OnDelta(contracts.DeltaContent, choice.Delta.Content)
				}
			}
			if choice.Delta.ReasoningContent != "" {
				reasoning.WriteString(choice.Delta.ReasoningContent)
				if opts.OnDelta != nil {
					opts.OnDelta(contracts.DeltaReasoning, choice.Delta.ReasoningContent)
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index < 0 {
					return contracts.ChatMessage{}, nil, fmt.Errorf("deepseek: invalid tool call index %d", delta.Index)
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
	if err := validateDeepSeekFinishReason(finishReason); err != nil {
		return contracts.ChatMessage{}, usage, err
	}

	text := content.String()
	message := contracts.ChatMessage{Role: "assistant", Content: &text, ReasoningContent: reasoning.String()}
	for _, call := range toolCalls {
		if call.ID != "" || call.Function.Name != "" || call.Function.Arguments != "" {
			message.ToolCalls = append(message.ToolCalls, call)
		}
	}
	return message, usage, nil
}

func normalizeDeepSeekReasoningEffort(effort string) (string, error) {
	switch effort {
	case "minimal", "low":
		return "low", nil
	case "medium", "high", "xhigh":
		return "high", nil
	case "max", "ultra":
		return "max", nil
	default:
		return "", fmt.Errorf("deepseek: unsupported reasoning effort %q", effort)
	}
}

func validateDeepSeekFinishReason(reason string) error {
	switch reason {
	case "stop", "tool_calls":
		return nil
	case "length":
		return errors.New("deepseek: response exceeded the output or context token limit")
	case "content_filter":
		return errors.New("deepseek: response was blocked by the content filter")
	case "insufficient_system_resource":
		return errors.New("deepseek: response was interrupted by insufficient provider resources")
	case "aborted":
		return errors.New("deepseek: response was aborted")
	case "":
		return errors.New("deepseek: response ended without a finish reason")
	default:
		return fmt.Errorf("deepseek: unsupported finish reason %q", reason)
	}
}
