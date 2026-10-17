package models

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const claudeTranscriptInstruction = "The JSON in the user message is the conversation so far, oldest first. " +
	"Reply only as the next assistant turn, answering through the supplied output schema."

// claudeNativeToolsDisabled closes what the output schema cannot: the CLI also
// exposes a native tool channel, and it is disabled (--tools ""). A model that
// reaches for it receives "No such tool available" and, without this rule,
// concludes that no tool exists at all. It goes last because the model weighs
// the final instruction most.
const claudeNativeToolsDisabled = "The native tool channel is disabled. " +
	"Do not emit native tool calls: they fail with \"No such tool available\". " +
	"Every action must be requested in the tool_calls array of the output object; leave it empty when no tool is needed."

// claudeSystemPrompt joins infai's own system messages with the instruction that
// frames the transcript. Tool definitions travel in the output schema, not here.
func claudeSystemPrompt(messages []contracts.ChatMessage) string {
	parts := make([]string, 0, 2)
	for _, message := range messages {
		if message.Role == "system" && message.Content != nil && *message.Content != "" {
			parts = append(parts, *message.Content)
		}
	}
	parts = append(parts, claudeTranscriptInstruction, claudeNativeToolsDisabled)
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
	Role       string               `json:"role"`
	Content    *string              `json:"content,omitempty"`
	ToolCalls  []contracts.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	Name       *string              `json:"name,omitempty"`
	Status     string               `json:"status,omitempty"`
}

// claudeTranscriptText serializes the conversation as JSON. It deliberately
// carries no timestamps, no random identifiers and no map iteration, so two
// turns that share a prefix produce a byte-identical shared prefix.
func claudeTranscriptText(messages []contracts.ChatMessage) string {
	conversation := make([]claudeTranscriptMessage, 0, len(messages))
	for _, message := range messages {
		entry := claudeTranscriptMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  message.ToolCalls,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
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
	return string(raw)
}
