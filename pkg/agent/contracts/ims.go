package contracts

// ChatMessage is the single source of truth for chat messages and is
// serialized directly as the OpenAI-compatible wire format. Adapters must
// not define their own parallel message types.
type ChatMessage struct {
	Role             string  `json:"role"`
	Content          *string `json:"content,omitempty"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
	Name             *string `json:"name,omitempty"`
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

type SessionMemory interface {
	Export() error
	Create() (string, error)
	Delete(sessId string) error
	List() error
	Load(sessId string) error
	SaveAsync(sessId string) (appendOnlyWriter chan<- []ChatMessage, closer func() any, err error)
	Compact(sessId string) error
	// tree
}
