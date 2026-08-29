package contracts

// ChatMessage is the single source of truth for chat messages and is
// serialized directly as the OpenAI-compatible wire format. Adapters must
// not define their own parallel message types.
type ChatMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Name             *string    `json:"name,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is a function-call the model requested. Schema is defined now;
// the tool loop (AccessControl → execution → results) wires it later.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function names the tool and carries the JSON-encoded argument object.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
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

func NewAssistantMessage(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: &content}
}

// Skill is a capability the model can apply (knowledge/memory), described
// for the system prompt. Skills live with memory because they are learned
// capabilities rather than executable actions.
type Skill struct {
	Title       string
	Description string
}

// DeepKnowledge we can store Skills

type DeepKnowledgeMemory interface {
	Query() (ChatMessage, error)
	// Memorize for storage which will be picked up by Memorize to store
	Memorize(string)
	// Dream for proper storage just like human brain
	Dream(string)
}

type LongTermMemory interface {
	Remember() (ChatMessage, error)
	Learn(string)
}

// SessionMemory is the persistence contract for a session transcript.
type SessionMemory interface {
	Load(sessID string) ([]ChatMessage, error)
	Append(sessID string, messages ...ChatMessage) error
	Delete(sessID string) error
}
