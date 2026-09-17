package contracts

// Skill is a capability the model can apply (knowledge/memory), described
// for the system prompt. Skills live with memory because they are learned
// capabilities rather than executable actions.
type Skill struct {
	Title       string
	Description string
	Location    string
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
