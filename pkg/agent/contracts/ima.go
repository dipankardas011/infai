package contracts

import "context"

// InfaiModelAdaptor is the primitive model contract. The agent feeds it the
// running conversation and it returns the next assistant message.
//
// The "proper" saga (InitializeSession, EstablishRESTConnection, ListModels,
// streaming, cost tracking, …) comes back on top of this once the primitive
// loop is proven out.
type InfaiModelAdaptor interface {
	// Generate returns the next assistant message for the given history.
	// A nil opts means "use adapter defaults".
	Generate(ctx context.Context, messages []ChatMessage, opts *GenerateOptions) (ChatMessage, error)
}

// GenerateOptions carries per-request provider knobs. Zero values mean
// "use the adapter default". Thinking budget / reasoning effort are per-turn
// settings, whereas the emitted reasoning text itself lives on
// ChatMessage.Thinking so it is echoed back across turns.
type GenerateOptions struct {
	MaxTokens            int
	Temperature          float64
	ThinkingBudgetTokens int
	ReasoningEffort      string
}

// ChatCompletion()
