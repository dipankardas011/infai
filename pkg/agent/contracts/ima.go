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
	Generate(ctx context.Context, messages []ChatMessage, tools []Tool, opts *GenerateOptions) (ChatMessage, *TokenUsage, error)
}

// TokenUsage is the provider-reported token accounting for one request.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// GenerateOptions carries per-request provider knobs.
type GenerateOptions struct {
	MaxTokens            int
	Temperature          float64
	ThinkingBudgetTokens int
	ReasoningEffort      string

	// Stream asks the adapter to stream output as it is generated. Deltas
	// (typed by DeltaKind, in stream order) are delivered to OnDelta; the
	// full message is still returned as usual.
	Stream  bool
	OnDelta func(kind DeltaKind, text string)
}

// DeltaKind distinguishes the text fragments a stream delivers.
type DeltaKind string

const (
	// DeltaContent is the model's visible answer text.
	DeltaContent DeltaKind = "content"
	// DeltaReasoning is the model's reasoning text (shown separately).
	DeltaReasoning DeltaKind = "reasoning"
	// DeltaStatus is a live UI status update, not model output.
	DeltaStatus DeltaKind = "status"
	// DeltaCompactionSummary is a live-only compaction summary for the UI.
	DeltaCompactionSummary DeltaKind = "compaction_summary"
	// DeltaToolCall identifies a tool invocation requested by the model.
	DeltaToolCall DeltaKind = "tool_call"
	// DeltaToolResult identifies the completion of a tool invocation.
	DeltaToolResult DeltaKind = "tool_result"
)

// ChatCompletion()
