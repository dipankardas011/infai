package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

var (
	ErrProviderNotLoggedIn = errors.New("provider is not logged in")
	ErrProviderLoggedIn    = errors.New("provider is already logged in")
)

func (e *InfaiAgentEngine) ListAllProviderModels() []glue.ListModelOutput {
	e.mu.Lock()
	defer e.mu.Unlock()

	providerModels := make([]glue.ListModelOutput, 0)
	for providerName, provider := range e.providers.Providers {
		for modelName, model := range provider.Models {
			displayName := modelName
			providerModels = append(providerModels, glue.ListModelOutput{
				ModelName:      displayName,
				ModelID:        model.Id,
				ProviderName:   providerName,
				ContextWindow:  model.MaxContextLength,
				ThinkingLevels: model.AvailableThinkingPatterns(),
			})
		}
	}
	sort.Slice(providerModels, func(i, j int) bool {
		if providerModels[i].ProviderName == providerModels[j].ProviderName {
			return providerModels[i].ModelName < providerModels[j].ModelName
		}
		return providerModels[i].ProviderName < providerModels[j].ProviderName
	})
	return providerModels
}

func (e *InfaiAgentEngine) Provider(name string) (contracts.LLMProviderConfiguration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	provider, ok := e.providers.Providers[name]
	return provider, ok
}

func (e *InfaiAgentEngine) LoginProvider(ctx context.Context, input glue.LoginProviderInput) error {
	providerName := string(input.ProviderID)
	if input.ProviderID != contracts.Codex && input.ProviderID != contracts.DeepSeek {
		return fmt.Errorf("provider %q does not support managed login", input.ProviderID)
	}
	e.mu.Lock()
	_, exists := e.providers.Providers[providerName]
	e.mu.Unlock()
	if exists {
		return fmt.Errorf("%w: %q", ErrProviderLoggedIn, providerName)
	}

	providers, err := models.GetAllInfaiManagedProviderConfigs(ctx, []contracts.ProviderSlug{input.ProviderID})
	if err != nil {
		return err
	}
	provider, ok := providers[input.ProviderID]
	if !ok {
		return fmt.Errorf("provider %q was not returned by the Infai catalog", input.ProviderID)
	}
	if err := validateProvider(providerName, provider); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.providers.Providers[providerName]; exists {
		return fmt.Errorf("%w: %q", ErrProviderLoggedIn, providerName)
	}
	e.providers.Providers[providerName] = provider
	if err := store.PersistProviders(e.providers); err != nil {
		delete(e.providers.Providers, providerName)
		return err
	}
	return nil
}

func (e *InfaiAgentEngine) LogoutProvider(input glue.LogoutProviderInput) error {
	providerName := string(input.ProviderID)
	if input.ProviderID != contracts.Codex && input.ProviderID != contracts.DeepSeek {
		return fmt.Errorf("provider %q does not support managed logout", input.ProviderID)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	provider, exists := e.providers.Providers[providerName]
	if !exists {
		return fmt.Errorf("%w: %q", ErrProviderNotLoggedIn, providerName)
	}
	delete(e.providers.Providers, providerName)
	if err := store.PersistProviders(e.providers); err != nil {
		e.providers.Providers[providerName] = provider
		return err
	}
	return nil
}

func validateProvider(name string, provider contracts.LLMProviderConfiguration) error {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return errors.New("provider name is required")
	}
	if name != trimmedName {
		return errors.New("provider name cannot have surrounding whitespace")
	}
	switch provider.Id {
	case contracts.Codex, contracts.DeepSeek:
		if name != string(provider.Id) {
			return fmt.Errorf("managed provider %q must use its provider ID as its name", provider.Id)
		}
		if provider.BaseEndpoint == "" || provider.APIType == "" || len(provider.Models) == 0 {
			return fmt.Errorf("managed provider %q has incomplete catalog configuration", provider.Id)
		}
	case contracts.OpenAIGeneric:
		if name == string(contracts.Codex) || name == string(contracts.DeepSeek) {
			return fmt.Errorf("generic provider cannot use reserved name %q", name)
		}
		endpoint, err := url.Parse(provider.BaseEndpoint)
		if err != nil || !endpoint.IsAbs() || endpoint.Host == "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("generic provider %q requires an absolute HTTP(S) base endpoint", name)
		}
		if provider.APIType != contracts.OpenAICompatableAPI {
			return fmt.Errorf("generic provider %q has unsupported API type %q", name, provider.APIType)
		}
	default:
		return fmt.Errorf("provider %q has unsupported ID %q", name, provider.Id)
	}
	for modelName, model := range provider.Models {
		if model.DefaultTemperature != nil && (*model.DefaultTemperature < 0 || *model.DefaultTemperature > 2) {
			return fmt.Errorf("model %q for provider %q has temperature outside the OpenAI-compatible range 0..2", modelName, name)
		}
	}
	return nil
}
