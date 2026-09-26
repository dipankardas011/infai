package contracts

// ChatOptions carries per-chat knobs.
type ChatOptions struct {
	Thinking InfaiThinkingLevel
}

// ChatMessage is the single source of truth for chat messages.
// NOTE: Adapters remove harness-only fields such as Status before sending the OpenAI wire format.
type ChatMessage struct {
	Role               string              `json:"role"`
	Content            *string             `json:"content,omitempty"`
	Images             []ImageInput        `json:"images,omitempty"`
	ReasoningContent   string              `json:"reasoning_content,omitempty"`
	ReasoningSignature string              `json:"reasoning_signature,omitempty"`
	Name               *string             `json:"name,omitempty"`
	ToolCallID         string              `json:"tool_call_id,omitempty"`
	ToolCalls          []ToolCall          `json:"tool_calls,omitempty"`
	Status             ToolExecutionStatus `json:"status,omitempty"`
}

// Text returns the message content, or "" when the message carried none.
func (m ChatMessage) Text() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

func NewSystemMessage(content string) ChatMessage {
	return ChatMessage{Role: "system", Content: &content}
}

func NewUserMessage(content string) ChatMessage {
	return ChatMessage{Role: "user", Content: &content}
}

// NewUserMessageWithInput builds a user message carrying text and image
// attachments. Image bytes stay in canonical form; adapters translate them.
func NewUserMessageWithInput(input UserInput) ChatMessage {
	content := input.Text
	return ChatMessage{Role: "user", Content: &content, Images: input.Images}
}

func NewAssistantMessage(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: &content}
}
