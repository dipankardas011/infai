package models

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const claudeNativeToolsDisabled = "The native tool channel is disabled. " +
	"Do not emit native tool calls: they fail with \"No such tool available\". " +
	"Every action must be requested in the tool_calls array of the output object; leave it empty when no tool is needed."

func claudeSystemPrompt(messages []contracts.ChatMessage) string {
	var parts []string
	for _, message := range messages {
		if message.Role == "system" && message.Content != nil && *message.Content != "" {
			parts = append(parts, *message.Content)
		}
	}
	parts = append(parts, claudeNativeToolsDisabled)
	return strings.Join(parts, "\n\n")
}

type claudeTranscript struct {
	Messages []claudeTranscriptMessage `json:"messages"`
}

// claudeTranscriptMessage is the wire shape handed to the CLI. It is a stable,
// append-only projection of contracts.ChatMessage: the same history must
// serialize byte-identically on every turn so the CLI can reuse its own prompt
// cache across separate processes. Field order and key order are therefore
// fixed by the struct and by encoding/json.
type claudeTranscriptMessage struct {
	Role       string                     `json:"role"`
	Content    *string                    `json:"content,omitempty"`
	Images     []claudeTranscriptImage    `json:"images,omitempty"`
	ToolCalls  []claudeTranscriptToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	Name       *string                    `json:"name,omitempty"`
	Status     string                     `json:"status,omitempty"`
}

// claudeTranscriptImage points at an image content block carried outside the
// transcript JSON. Image bytes cannot travel inside a text block, so the
// transcript names each block's position and the CLI receives the blocks
// themselves in the same order.
type claudeTranscriptImage struct {
	Index     int    `json:"index"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type"`
}

// claudeTranscriptToolCall mirrors the CLI's native tool_use block: a name and
// an input object. Replaying the OpenAI function.arguments string here gives
// the model a second tool-call dialect, and it occasionally merges the two
// into arguments: {arguments: "..."} instead of the requested fields.
type claudeTranscriptToolCall struct {
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func claudeTranscriptToolCalls(calls []contracts.ToolCall) []claudeTranscriptToolCall {
	projected := make([]claudeTranscriptToolCall, 0, len(calls))
	for _, call := range calls {
		input := json.RawMessage(`{}`)
		var object map[string]any
		if json.Unmarshal([]byte(call.Function.Arguments), &object) == nil && object != nil {
			input = json.RawMessage(call.Function.Arguments)
		}
		projected = append(projected, claudeTranscriptToolCall{
			ID:    call.ID,
			Name:  string(call.Function.Name),
			Input: input,
		})
	}
	return projected
}

func claudeConversation(messages []contracts.ChatMessage) []contracts.ChatMessage {
	conversation := make([]contracts.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		conversation = append(conversation, message)
	}
	return conversation
}

// claudeTranscriptText serializes the conversation as JSON. It deliberately
// carries no timestamps, no random identifiers and no map iteration, so two
// turns that share a prefix produce a byte-identical shared prefix.
func claudeTranscriptText(messages []contracts.ChatMessage) string {
	text, _ := claudeTranscriptParts(claudeConversation(messages))
	return text
}

func claudeTranscriptParts(messages []contracts.ChatMessage) (string, []contracts.ImageInput) {
	conversation := make([]claudeTranscriptMessage, 0, len(messages))
	images := make([]contracts.ImageInput, 0)
	for _, message := range messages {
		entry := claudeTranscriptMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  claudeTranscriptToolCalls(message.ToolCalls),
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		}
		for _, image := range message.Images {
			entry.Images = append(entry.Images, claudeTranscriptImage{
				Index:     len(images),
				Name:      image.Name,
				MediaType: image.MediaType,
			})
			images = append(images, image)
		}
		if message.Status != "" {
			entry.Status = string(message.Status)
		}
		conversation = append(conversation, entry)
	}
	raw, err := json.Marshal(claudeTranscript{Messages: conversation})
	if err != nil {
		// A ChatMessage tree of strings, pointers and slices cannot fail to
		// marshal; reaching here means the contract changed under us.
		panic(fmt.Sprintf("claude cli: serialize transcript: %v", err))
	}
	return string(raw), images
}

// claudeStreamUserMessage is the second line of the CLI's stream-json input.
// Every turn uses this envelope: the first line is the initialize control
// request, and control requests exist only in stream-json mode. It is also the
// only mode that accepts image blocks.
type claudeStreamUserMessage struct {
	Type            string              `json:"type"`
	Message         claudeStreamMessage `json:"message"`
	ParentToolUseID any                 `json:"parent_tool_use_id"`
	SessionID       string              `json:"session_id"`
}

type claudeStreamMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type claudeImageBlock struct {
	Type   string            `json:"type"`
	Source claudeImageSource `json:"source"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type claudeTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// claudeStdinInput is everything one turn writes to the CLI's stdin, in wire
// order: the initialize control request, then the user message.
type claudeStdinInput struct {
	// SystemPrompt and Schema ride the initialize request because argv cannot
	// carry them (§ claudeCLIArgs).
	SystemPrompt string
	Schema       string
	RequestID    string
	SessionID    string
	Transcript   string
	Images       []contracts.ImageInput
}

// claudeStreamInit is the CLI's control request envelope. The initialize
// subtype is the only channel that carries a system prompt and an output
// schema without an argument.
type claudeStreamInit struct {
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	Request   claudeInitRequest `json:"request"`
}

type claudeInitRequest struct {
	Subtype      string          `json:"subtype"`
	SystemPrompt string          `json:"systemPrompt,omitempty"`
	JSONSchema   json.RawMessage `json:"jsonSchema,omitempty"`
}

// claudeStdin returns the newline-delimited stdin for one turn.
func claudeStdin(in claudeStdinInput) string {
	schema := json.RawMessage(nil)
	if in.Schema != "" {
		schema = json.RawMessage(in.Schema)
	}
	initLine, err := json.Marshal(claudeStreamInit{
		Type:      "control_request",
		RequestID: in.RequestID,
		Request: claudeInitRequest{
			Subtype:      "initialize",
			SystemPrompt: in.SystemPrompt,
			JSONSchema:   schema,
		},
	})
	if err != nil {
		// The schema is JSON already (claudeToolSchema marshalled it); reaching
		// here means the contract changed under us.
		panic(fmt.Sprintf("claude cli: serialize the initialize request: %v", err))
	}

	content := make([]any, 0, len(in.Images)+1)
	for _, image := range in.Images {
		content = append(content, claudeImageBlock{
			Type: "image",
			Source: claudeImageSource{
				Type:      "base64",
				MediaType: image.MediaType,
				Data:      base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	// Anthropic recommends images before the text they accompany.
	content = append(content, claudeTextBlock{Type: "text", Text: in.Transcript})
	userLine, err := json.Marshal(claudeStreamUserMessage{
		Type:            "user",
		Message:         claudeStreamMessage{Role: "user", Content: content},
		ParentToolUseID: nil,
		SessionID:       in.SessionID,
	})
	if err != nil {
		panic(fmt.Sprintf("claude cli: serialize stream-json input: %v", err))
	}
	return string(initLine) + "\n" + string(userLine) + "\n"
}
