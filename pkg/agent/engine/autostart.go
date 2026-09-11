package engine

import (
	"context"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/ds"
)

func (e *InfaiAgentEngine) LoadConfiguredProviders(ctx context.Context) error {
	e.bgLogger.InfoContext(ctx, "Fetching Provider details", "operation", "load_configured_providers")

	providerStore, err := store.LoadProviders()
	if err != nil {
		return err
	}

	fetchConfigurationFromInfai := ds.NewSet[contracts.ProviderSlug]()

	for providerName, providerConfig := range providerStore.Providers {
		switch providerConfig.Id {
		case contracts.OpenAIGeneric:
		case contracts.Codex, contracts.DeepSeek:
			fetchConfigurationFromInfai.Add(providerConfig.Id)
		default:
			return fmt.Errorf("provider %q has unsupported ID %q", providerName, providerConfig.Id)
		}
	}

	if fetchConfigurationFromInfai.Size() == 0 {
		for providerName, providerConfig := range providerStore.Providers {
			if err := validateProvider(providerName, providerConfig); err != nil {
				return err
			}
		}
		e.providerMu.Lock()
		e.providers = providerStore
		e.providerMu.Unlock()
		return nil
	}

	infaiManagedWellKnownProviders, err := models.GetAllInfaiManagedProviderConfigs(ctx, fetchConfigurationFromInfai.ToSlice())
	if err != nil {
		return err
	}

	providers := make(map[string]contracts.LLMProviderConfiguration, len(providerStore.Providers))
	for providerName, providerConfig := range providerStore.Providers {
		v := providerConfig

		if providerConfig.Id != contracts.OpenAIGeneric {
			managed, ok := infaiManagedWellKnownProviders[providerConfig.Id]
			if !ok {
				return fmt.Errorf("provider %q was not returned by the Infai catalog", providerConfig.Id)
			}
			v.BaseEndpoint = managed.BaseEndpoint
			v.APIType = managed.APIType
			v.Models = managed.Models
		}

		providers[providerName] = v
	}
	for providerName, providerConfig := range providers {
		if err := validateProvider(providerName, providerConfig); err != nil {
			return err
		}
	}
	e.providerMu.Lock()
	e.providers.Providers = providers
	e.providerMu.Unlock()

	return nil
}
