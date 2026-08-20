package contracts

import "context"

// InfaiModelAdaptor is the primitive model contract. The agent feeds it the
// running conversation and it returns the next assistant message.
//
// The "proper" saga (InitializeSession, EstablishRESTConnection, ListModels,
// streaming, cost tracking, …) comes back on top of this once the primitive
// loop is proven out.
type InfaiModelAdaptor interface {
	// Generate returns the next assistant message for the given history plus
	// the request's token usage (nil when the provider reports none).
	Generate(ctx context.Context, messages []ChatMessage, opts *GenerateOptions) (ChatMessage, *TokenUsage, error)
}

// TokenUsage is the provider-reported token accounting for one request.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add sums another usage into t. Nil-safe; use it to accumulate usage across
// every turn of a multi-turn agent run.
func (t *TokenUsage) Add(o *TokenUsage) {
	if t == nil || o == nil {
		return
	}
	t.PromptTokens += o.PromptTokens
	t.CompletionTokens += o.CompletionTokens
	t.TotalTokens += o.TotalTokens
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
