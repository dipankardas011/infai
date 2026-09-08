package glue

import "github.com/dipankardas011/infai/pkg/agent/contracts"

type LoginProviderInput struct {
	ProviderID contracts.ProviderSlug `json:"provider_id"`
}

type LogoutProviderInput struct {
	ProviderID contracts.ProviderSlug `json:"provider_id"`
}

type ListModelOutput struct {
	ModelName      string   `json:"model_name"`
	ModelID        string   `json:"model_id"`
	ProviderName   string   `json:"provider_name"`
	ContextWindow  uint64   `json:"context_window"`
	ThinkingModels []string `json:"thinking_models"`
}
