package models

import (
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// NewLLMProvider constructs the stateful provider implementation declared by
// config. Provider-specific behavior remains inside that implementation.
func NewLLMProvider(config contracts.ProviderConfig, state contracts.ProviderStateStore) (contracts.LLMProvider, error) {
	if config.ID == contracts.ProviderIDOpenAICodex {
		return newOpenAICodexProvider(config, state)
	}
	return newGenericOpenAIProvider(config, state)
}

func cloneProviderConfig(config contracts.ProviderConfig) contracts.ProviderConfig {
	if config.Models == nil {
		return config
	}
	config.Models = make(map[string]contracts.ProviderModel, len(config.Models))
	for slug, model := range config.Models {
		model.Input = append([]string(nil), model.Input...)
		model.ThinkingModes = append([]string(nil), model.ThinkingModes...)
		if model.ThinkingLevelMap != nil {
			mapping := make(map[string]string, len(model.ThinkingLevelMap))
			for level, value := range model.ThinkingLevelMap {
				mapping[level] = value
			}
			model.ThinkingLevelMap = mapping
		}
		config.Models[slug] = model
	}
	return config
}
