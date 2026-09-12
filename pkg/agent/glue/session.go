package glue

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// SessionOutput combines durable session metadata with runtime model capacity.
type SessionOutput struct {
	store.SessionMeta
	ContextWindow     uint64                           `json:"context_window"`
	Thinking          contracts.InfaiThinkingLevel     `json:"thinking"`
	AvailableThinking []contracts.InfaiThinkingLevel   `json:"available_thinking"`
	Modalities        []contracts.LLMSupportedModality `json:"modalities"`
}
