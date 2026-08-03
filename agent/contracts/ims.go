package contracts

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// DeepKnowledge we can store Skills

type DeepKnowledgeMemory interface {
	Query() (ChatMessage, error)
	Memorize(string)
}

type LongTermMemory interface {
	Remember() (ChatMessage, error)
	Learn(string)
}

type SessionMemory interface {
	Export() error
	Create() error
	Delete() error
	List() error
	Load() error
}
