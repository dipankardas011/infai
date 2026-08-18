package contracts

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
