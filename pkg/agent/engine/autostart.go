package engine

import (
	"context"
	"maps"

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

	for _, providerConfig := range providerStore.Providers {
		if providerConfig.Id != contracts.OpenAIGeneric {
			fetchConfigurationFromInfai.Add(providerConfig.Id)
		}
	}

	if fetchConfigurationFromInfai.Size() == 0 {
		maps.Copy(e.providers.Providers, providerStore.Providers)
		return nil
	}

	infaiManagedWellKnownProviders, err := models.GetAllInfaiManagedProviderConfigs(ctx, fetchConfigurationFromInfai.ToSlice())
	if err != nil {
		return err
	}

	e.providers.Providers = make(map[string]contracts.LLMProviderConfiguration)
	for providerName, providerConfig := range providerStore.Providers {
		v := providerConfig

		if providerConfig.Id != contracts.OpenAIGeneric {
			v.BaseEndpoint = infaiManagedWellKnownProviders[providerConfig.Id].BaseEndpoint
			v.APIType = infaiManagedWellKnownProviders[providerConfig.Id].APIType
			maps.Copy(v.Models, infaiManagedWellKnownProviders[providerConfig.Id].Models)
		}

		e.providers.Providers[providerName] = v
	}

	return nil
}
