package glue

import "github.com/dipankardas011/infai/pkg/agent/contracts"

type LoginProviderInput struct {
	ProviderID contracts.ProviderSlug          `json:"provider_id"`
	Method     contracts.LLMProviderAuthMethod `json:"method"`
	Credential string                          `json:"credential,omitempty"`
}

type LoginProviderOutput struct {
	Status    contracts.ProviderAuthStatus    `json:"status"`
	Challenge contracts.ProviderAuthChallenge `json:"challenge,omitempty"`
	Error     string                          `json:"error,omitempty"`
}

type LogoutProviderInput struct {
	ProviderID contracts.ProviderSlug `json:"provider_id"`
}

type ListModelOutput struct {
	ModelName      string                           `json:"model_name"`
	ModelID        string                           `json:"model_id"`
	ProviderName   string                           `json:"provider_name"`
	ContextWindow  uint64                           `json:"context_window"`
	ThinkingLevels []contracts.InfaiThinkingLevel   `json:"thinking_levels"`
	Modalities     []contracts.LLMSupportedModality `json:"modalities"`
}
