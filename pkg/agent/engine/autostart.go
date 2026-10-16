package engine

import (
	"context"
	"fmt"
	"time"

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
			v = withManagedCatalog(v, managed)
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

const managedProviderCatalogInterval = time.Hour

func (e *InfaiAgentEngine) watchManagedProviderCatalogs() {
	ticker := time.NewTicker(managedProviderCatalogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.refreshManagedProviderCatalogs(e.ctx)
		}
	}
}

// refreshManagedProviderCatalogs replaces what the served catalog says about
// each managed provider — its base endpoint, API type and models — and leaves
// everything else where it is: credentials are not in the served catalog, and a
// generic provider is the operator's own.
//
// This runs against a live agent, so unlike startup it cannot fail loudly: a
// host that is briefly unreachable, or a catalog the validator rejects, leaves
// the catalog already in hand in place and says so in the log. A running session
// is untouched either way, because a session holds the model it was opened with.
func (e *InfaiAgentEngine) refreshManagedProviderCatalogs(ctx context.Context) {
	configured := ds.NewSet[contracts.ProviderSlug]()

	e.providerMu.Lock()
	for _, provider := range e.providers.Providers {
		if provider.Id == contracts.Codex || provider.Id == contracts.DeepSeek {
			configured.Add(provider.Id)
		}
	}
	e.providerMu.Unlock()

	ids := configured.ToSlice()
	if len(ids) == 0 {
		return
	}

	// Read outside the lock: a login takes the same one, and a slow host must not
	// be able to answer a login with "another provider operation is already
	// running". A provider that failed to answer is simply absent from the
	// result, and so keeps the catalog it already has.
	fetched, err := models.GetAllInfaiManagedProviderConfigs(ctx, ids)
	if err != nil {
		e.bgLogger.WarnContext(ctx, "provider catalog refresh could not read every catalog", "error", err)
	}

	e.providerMu.Lock()
	defer e.providerMu.Unlock()

	merged := make(map[string]contracts.LLMProviderConfiguration, len(e.providers.Providers))
	for providerName, provider := range e.providers.Providers {
		managed, ok := fetched[provider.Id]
		if !ok {
			merged[providerName] = provider
			continue
		}
		candidate := withManagedCatalog(provider, managed)
		if err := validateProvider(providerName, candidate); err != nil {
			e.bgLogger.WarnContext(ctx, "provider catalog refresh rejected, keeping the catalog in hand",
				"provider", providerName, "error", err)
			return
		}
		merged[providerName] = candidate
	}

	e.providers = contracts.LLMProviders{Providers: merged}
	e.bgLogger.InfoContext(ctx, "provider catalogs refreshed", "providers", configured.Size())
}

// withManagedCatalog returns the provider carrying the base endpoint, API type
// and models Infai serves for it. The credentials stay the local entry's: the
// served catalog carries none, and the local one may hold a token refreshed a
// moment ago.
func withManagedCatalog(provider, managed contracts.LLMProviderConfiguration) contracts.LLMProviderConfiguration {
	provider.BaseEndpoint = managed.BaseEndpoint
	provider.APIType = managed.APIType
	provider.Models = managed.Models
	return provider
}
