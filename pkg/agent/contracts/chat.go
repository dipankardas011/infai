package contracts

// ChatOptions carries per-chat knobs.
type ChatOptions struct {
	Thinking InfaiThinkingLevel
}

// ChatMessage is the single source of truth for chat messages.
// NOTE: Adapters remove harness-only fields such as Status before sending the OpenAI wire format.
type ChatMessage struct {
	Role               string              `json:"role"`
	ClientMessageID    string              `json:"client_message_id,omitempty"`
	Content            *string             `json:"content,omitempty"`
	Images             []ImageInput        `json:"images,omitempty"`
	Artifacts          []Artifact          `json:"artifacts,omitempty"`
	ReasoningContent   string              `json:"reasoning_content,omitempty"`
	ReasoningSignature string              `json:"reasoning_signature,omitempty"`
	Name               *string             `json:"name,omitempty"`
	ToolCallID         string              `json:"tool_call_id,omitempty"`
	ToolCalls          []ToolCall          `json:"tool_calls,omitempty"`
	Status             ToolExecutionStatus `json:"status,omitempty"`
	Eval               *EvalResult         `json:"eval,omitempty"`
}

// EvalResult is the outcome of running a sidecar's acceptance script against
// one assistant answer. It rides on that message, so the timeline keeps the
// per-attempt history without a separate record kind.
type EvalResult struct {
	Passed  bool   `json:"passed"`
	Attempt int    `json:"attempt,omitempty"`
	Output  string `json:"output,omitempty"`
}

// AcceptanceScriptPrefix opens the chip line that reports an acceptance-script
// outcome. Success and failure share it, so the transcript recognizes the gate
// without a second marker and the model reads the same label the reader sees.
const AcceptanceScriptPrefix = "[acceptance_script "

// Artifact is a non-text payload the model produced, such as a generated
// image. Unlike Images it is never replayed to a provider.
type Artifact struct {
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	ItemID    string `json:"item_id,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

// HasArtifacts reports whether the message carries any produced artifact.
func (m ChatMessage) HasArtifacts() bool { return len(m.Artifacts) > 0 }

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

// NewUserMessageWithInput builds a user message carrying text, image
// attachments, and the client's own id for the turn. Image bytes stay in
// canonical form; adapters translate them, and the id never reaches a provider.
func NewUserMessageWithInput(input UserInput) ChatMessage {
	content := input.Text
	return ChatMessage{Role: "user", ClientMessageID: input.ClientMessageID, Content: &content, Images: input.Images}
}

func NewAssistantMessage(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: &content}
}

// Need this for the agent taking the response.
// NewSidecarAgentResponse is a message from a sidecar to its caller: the answer
// text, attributed to the sidecar by name.
func NewSidecarAgentResponse(content string, attributedTo string) ChatMessage {
	return ChatMessage{Role: "user", Content: &content, Name: &attributedTo}
}
