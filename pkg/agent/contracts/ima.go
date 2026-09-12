package contracts

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// InfaiModelAdaptor is the primitive model contract. The agent feeds it the
// running conversation and it returns the next assistant message.
type InfaiModelAdaptor interface {
	// Generate returns the next assistant message for the given history plus
	// the request's token usage (nil when the provider reports none).
	Generate(ctx context.Context, messages []ChatMessage, tools []Tool, opts *GenerateOptions) (ChatMessage, *TokenUsage, error)

	GetModelSpecs() ProvisionedModel
}

// Model provider cannot override Readonly!
type ProvisionedModel struct {
	providerId   ProviderSlug
	providerName string
	baseEndpoint string
	apiType      ProviderAPIType
	auth         LLMProviderAuth
	model        LLMModelConfiguration
	thinking     InfaiThinkingLevel
}

func NewProvisionedModel(providerId ProviderSlug, providerName string, baseEndpoint string, apiType ProviderAPIType, auth LLMProviderAuth, model LLMModelConfiguration) ProvisionedModel {
	return ProvisionedModel{providerId: providerId, providerName: providerName, baseEndpoint: baseEndpoint, apiType: apiType, auth: auth, model: model}
}
func (model ProvisionedModel) ProviderSlug() ProviderSlug          { return model.providerId }
func (model ProvisionedModel) ProviderName() string                { return model.providerName }
func (model ProvisionedModel) BaseEndpoint() string                { return model.baseEndpoint }
func (model ProvisionedModel) APIType() ProviderAPIType            { return model.apiType }
func (model ProvisionedModel) Auth() LLMProviderAuth               { return model.auth }
func (model ProvisionedModel) Model() LLMModelConfiguration        { return model.model }
func (model ProvisionedModel) ThinkingPattern() InfaiThinkingLevel { return model.thinking }

func (model ProvisionedModel) WithThinkingPattern(pattern InfaiThinkingLevel) (ProvisionedModel, error) {
	if pattern == "" {
		model.thinking = ""
		return model, nil
	}
	if slices.Contains(model.model.AvailableThinkingPatterns(), pattern) {
		model.thinking = pattern
		return model, nil
	}
	return ProvisionedModel{}, fmt.Errorf("thinking pattern %q is not supported by model %q", pattern, model.model.Id)
}

func (model ProvisionedModel) ThinkingLevelValue() (string, bool) {
	var value *string
	switch model.thinking {
	case ThinkingOff:
		value = model.model.ThinkingLevels.Off
	case ThinkingMinimal:
		value = model.model.ThinkingLevels.Minimal
	case ThinkingLow:
		value = model.model.ThinkingLevels.Low
	case ThinkingMedium:
		value = model.model.ThinkingLevels.Medium
	case ThinkingHigh:
		value = model.model.ThinkingLevels.High
	case ThinkingXHigh:
		value = model.model.ThinkingLevels.XHigh
	case ThinkingMax:
		value = model.model.ThinkingLevels.Max
	}
	if value == nil {
		return "", false
	}
	return *value, true
}

// TokenUsage is the provider-reported token accounting for one request.
type TokenUsage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	TotalTokens      uint64 `json:"total_tokens"`
}

// GenerateOptions carries per-request provider knobs.
type GenerateOptions struct {
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
	OpenAICompatableAPI     ProviderAPIType = "openai-completions"
	OpenAICodexResponsesAPI ProviderAPIType = "openai-codex-responses"
)

type LLMSupportedModality string

const (
	ModalityText  LLMSupportedModality = "text"
	ModalityImage LLMSupportedModality = "image"
	ModalityAudio LLMSupportedModality = "audio"
)

type LLMProviderAuthMethod string

const (
	OAuth2   LLMProviderAuthMethod = "oauth2"
	APIKey   LLMProviderAuthMethod = "api_key"
	NoneAuth LLMProviderAuthMethod = "none"
)

type LLMProviderConfiguration struct {
	Id           ProviderSlug                     `json:"id"`
	BaseEndpoint string                           `json:"base_endpoint"`
	APIType      ProviderAPIType                  `json:"api_type"`
	Auth         LLMProviderAuth                  `json:"auth"`
	Models       map[string]LLMModelConfiguration `json:"models"`
}

// LLMProviderAuth is the credential material persisted for a provider. Codex
// uses the OAuth fields; API-key providers use BearerToken.
type LLMProviderAuth struct {
	Method LLMProviderAuthMethod `json:"method"`

	// Oauth2
	AccessToken  string     `json:"access_token,omitempty"`
	RefreshToken string     `json:"refresh_token,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	AccountID    string     `json:"account_id,omitempty"`

	// API Token
	BearerToken string `json:"bearer_token,omitempty"`
}

type ThinkingLevels struct {
	// If its nil it means not supported
	// Value means what it means interms of the model provider the enum value of that provider
	Off     *string `json:"off"`
	Minimal *string `json:"minimal"`
	Low     *string `json:"low"`
	Medium  *string `json:"medium"`
	High    *string `json:"high"`
	XHigh   *string `json:"xhigh"`
	Max     *string `json:"max"`
}

// InfaiThinkingLevel is a provider-independent thinking selection.
// ThinkingLevels maps these values to provider wire values.
type InfaiThinkingLevel string

const (
	ThinkingOff     InfaiThinkingLevel = "off"
	ThinkingMinimal InfaiThinkingLevel = "minimal"
	ThinkingLow     InfaiThinkingLevel = "low"
	ThinkingMedium  InfaiThinkingLevel = "medium"
	ThinkingHigh    InfaiThinkingLevel = "high"
	ThinkingXHigh   InfaiThinkingLevel = "xhigh"
	ThinkingMax     InfaiThinkingLevel = "max"
)

type LLMModelConfiguration struct {
	Id                 string                 `json:"id"`
	Name               string                 `json:"name"`
	MaxContextLength   uint64                 `json:"max_context_window"`
	MaxOutputTokens    uint64                 `json:"max_output_tokens"`
	DefaultTemperature *float64               `json:"default_temperature"`
	Modality           []LLMSupportedModality `json:"modality"`
	AvailableThinking  bool                   `json:"available_thinking"`
	ThinkingLevels     ThinkingLevels         `json:"thinking_levels"`
}

func (m LLMModelConfiguration) AvailableThinkingPatterns() []InfaiThinkingLevel {
	if !m.AvailableThinking {
		return nil
	}

	patterns := make([]InfaiThinkingLevel, 0, 7)
	for _, level := range []struct {
		name  InfaiThinkingLevel
		value *string
	}{
		{ThinkingOff, m.ThinkingLevels.Off},
		{ThinkingMinimal, m.ThinkingLevels.Minimal},
		{ThinkingLow, m.ThinkingLevels.Low},
		{ThinkingMedium, m.ThinkingLevels.Medium},
		{ThinkingHigh, m.ThinkingLevels.High},
		{ThinkingXHigh, m.ThinkingLevels.XHigh},
		{ThinkingMax, m.ThinkingLevels.Max},
	} {
		if level.value != nil {
			patterns = append(patterns, level.name)
		}
	}
	return patterns
}
