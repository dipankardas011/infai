package contracts

import (
	"context"
	"time"
)

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
	// DeltaSkillLoad identifies a skill being loaded from memory into context.
	DeltaSkillLoad DeltaKind = "skill_load"
	// DeltaTaskChecklist carries the current structured task checklist state.
	DeltaTaskChecklist DeltaKind = "task_checklist"
)

type LLMProviders struct {
	// Key can be a userDefined as well as provider Slug (when user chooses deepseek/codex) else for a generic one they can add whatever they feel like
	Providers map[string]LLMProviderConfiguration `json:"providers"`
}

type ProviderSlug string

const (
	DeepSeek      ProviderSlug = "deepseek"
	Codex         ProviderSlug = "openai-codex"
	OpenAIGeneric ProviderSlug = "openai-generic"
)

type ProviderAPIType string

const (
	OpenAICompatableAPI ProviderAPIType = "openai-completions"
)

type LLMSupportedModality string

const (
	ModalityText  LLMSupportedModality = "text"
	ModalityImage LLMSupportedModality = "image"
	ModalityAudio LLMSupportedModality = "audio"
)

type ThinkingLevels string

const (
	OffThinking     ThinkingLevels = "off"
	MinimalThinking ThinkingLevels = "minimal"
	LowThinking     ThinkingLevels = "low"
	MediumThinking  ThinkingLevels = "medium"
	HighThinking    ThinkingLevels = "high"
	XHighThinking   ThinkingLevels = "xhigh"
	MaxThinking     ThinkingLevels = "max"
)

type LLMProviderAuthMethod string

const (
	OAuth2    LLMProviderAuthMethod = "oauth2"
	APIKey    LLMProviderAuthMethod = "api_key"
	NoneAuthh LLMProviderAuthMethod = "none"
)

type LLMProviderConfiguration struct {
	Id           ProviderSlug                     `json:"id"`
	BaseEndpoint string                           `json:"base_endpoint"`
	APIType      ProviderAPIType                  `json:"api_type"`
	Auth         LLMProviderAuth                  `json:"auth"`
	Models       map[string]LLMModelConfiguration `json:"models"`
}

type LLMProviderAuth struct {
	Method LLMProviderAuthMethod `json:"method"`

	// OAuth2
	ClientId     *string    `json:"client_id,omitempty"`
	ClientSecret *string    `json:"client_secret,omitempty"`
	TTLToken     *time.Time `json:"ttl_token,omitempty"`

	// APIKey
	BearerToken *string `json:"bearer_token,omitempty"`
}

type LLMModelConfiguration struct {
	Id                string                    `json:"id"`
	Name              string                    `json:"name"`
	MaxContextLength  uint64                    `json:"max_context_window"`
	MaxOutputTokens   uint64                    `json:"max_output_tokens"`
	Modality          []LLMSupportedModality    `json:"modality"`
	AvailableThinking bool                      `json:"available_thinking"`
	ThinkingLevelMap  map[ThinkingLevels]string `json:"thinking_level_map"`
}
