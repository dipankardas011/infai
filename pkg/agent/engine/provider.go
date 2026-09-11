package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

var (
	ErrProviderNotLoggedIn = errors.New("provider is not logged in")
	ErrProviderLoggedIn    = errors.New("provider is already logged in")
)

type providerAuthOperation struct {
	providerID contracts.ProviderSlug
	result     glue.LoginProviderOutput
	cancel     context.CancelFunc
}

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

func (e *InfaiAgentEngine) ProviderAuthMethods(providerID contracts.ProviderSlug) ([]contracts.ProviderAuthMethod, error) {
	return models.ProviderAuthMethods(providerID)
}

func (e *InfaiAgentEngine) LoginProvider(ctx context.Context, input glue.LoginProviderInput) (glue.LoginProviderOutput, error) {
	if input.Method == "" {
		return glue.LoginProviderOutput{}, errors.New("auth method is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	select {
	case <-e.stopCh:
		return glue.LoginProviderOutput{}, ErrEngineShuttingDown
	default:
	}

	providerName := string(input.ProviderID)
	existing, exists := e.providers.Providers[providerName]
	if exists && providerHasUsableAuth(existing) {
		return glue.LoginProviderOutput{}, fmt.Errorf("%w: %q", ErrProviderLoggedIn, providerName)
	}

	if e.providerAuth != nil && e.providerAuth.result.Status == contracts.ProviderAuthPending {
		return glue.LoginProviderOutput{}, fmt.Errorf("authentication for provider %q is already pending", e.providerAuth.providerID)
	}

	flowCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	state := &providerAuthOperation{
		providerID: input.ProviderID,
		result:     glue.LoginProviderOutput{Status: contracts.ProviderAuthPending},
		cancel:     cancel,
	}
	e.providerAuth = state

	authFlow, err := models.ProvisionProviderAuth(ctx, input.ProviderID, input.Method, input.Credential)
	if err != nil {
		cancel()
		state.result.Status = contracts.ProviderAuthFailed
		state.result.Error = err.Error()
		return glue.LoginProviderOutput{}, err
	}
	state.result.Challenge = authFlow.Challenge()
	result := state.result

	go e.completeProviderLogin(flowCtx, state, authFlow)
	return result, nil
}

func (e *InfaiAgentEngine) ProviderLoginStatus(providerID contracts.ProviderSlug) (glue.LoginProviderOutput, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.providerAuth == nil || e.providerAuth.providerID != providerID {
		return glue.LoginProviderOutput{}, false
	}
	return e.providerAuth.result, true
}

func (e *InfaiAgentEngine) completeProviderLogin(ctx context.Context, state *providerAuthOperation, authFlow contracts.ProviderAuthFlow) {
	defer state.cancel()
	auth, err := authFlow.Complete(ctx)
	if err == nil {
		err = e.persistManagedProvider(ctx, state.providerID, auth)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.stopCh:
		if err == nil {
			err = ErrEngineShuttingDown
		}
	default:
	}
	if err != nil {
		state.result.Status = contracts.ProviderAuthFailed
		state.result.Error = err.Error()
	} else {
		state.result.Status = contracts.ProviderAuthComplete
	}
}

func (e *InfaiAgentEngine) persistManagedProvider(ctx context.Context, providerID contracts.ProviderSlug, auth contracts.LLMProviderAuth) error {
	providers, err := models.GetAllInfaiManagedProviderConfigs(ctx, []contracts.ProviderSlug{providerID})
	if err != nil {
		return err
	}
	provider, ok := providers[providerID]
	if !ok {
		return fmt.Errorf("provider %q was not returned by the Infai catalog", providerID)
	}
	provider.Auth = auth
	providerName := string(providerID)
	if err := validateProvider(providerName, provider); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.stopCh:
		return ErrEngineShuttingDown
	default:
	}
	previous, exists := e.providers.Providers[providerName]
	if exists && providerHasUsableAuth(previous) {
		return fmt.Errorf("%w: %q", ErrProviderLoggedIn, providerName)
	}
	candidate := providersWith(e.providers, providerName, provider)
	if err := store.PersistProviders(candidate); err != nil {
		return err
	}
	e.providers = candidate
	return nil
}

func providersWith(current contracts.LLMProviders, name string, provider contracts.LLMProviderConfiguration) contracts.LLMProviders {
	candidate := contracts.LLMProviders{Providers: make(map[string]contracts.LLMProviderConfiguration, len(current.Providers)+1)}
	for configuredName, configured := range current.Providers {
		candidate.Providers[configuredName] = configured
	}
	candidate.Providers[name] = provider
	return candidate
}

func providerHasUsableAuth(provider contracts.LLMProviderConfiguration) bool {
	switch provider.Id {
	case contracts.Codex:
		return provider.Auth.Method == contracts.OAuth2 && strings.TrimSpace(provider.Auth.AccessToken) != "" &&
			strings.TrimSpace(provider.Auth.RefreshToken) != "" && provider.Auth.ExpiresAt != nil &&
			strings.TrimSpace(provider.Auth.AccountID) != ""
	case contracts.DeepSeek:
		return provider.Auth.Method == contracts.APIKey && strings.TrimSpace(provider.Auth.BearerToken) != ""
	default:
		return true
	}
}

func (e *InfaiAgentEngine) LogoutProvider(input glue.LogoutProviderInput) error {
	providerName := string(input.ProviderID)
	if input.ProviderID != contracts.Codex && input.ProviderID != contracts.DeepSeek {
		return fmt.Errorf("provider %q does not support managed logout", input.ProviderID)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	_, exists := e.providers.Providers[providerName]
	if !exists {
		return fmt.Errorf("%w: %q", ErrProviderNotLoggedIn, providerName)
	}
	candidate := contracts.LLMProviders{Providers: make(map[string]contracts.LLMProviderConfiguration, len(e.providers.Providers)-1)}
	for name, configured := range e.providers.Providers {
		if name != providerName {
			candidate.Providers[name] = configured
		}
	}
	if err := store.PersistProviders(candidate); err != nil {
		return err
	}
	e.providers = candidate
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
	case contracts.Codex:
		if name != string(provider.Id) {
			return fmt.Errorf("managed provider %q must use its provider ID as its name", provider.Id)
		}
		if provider.BaseEndpoint == "" || provider.APIType == "" || len(provider.Models) == 0 {
			return fmt.Errorf("managed provider %q has incomplete catalog configuration", provider.Id)
		}
		if provider.APIType != contracts.OpenAICodexResponsesAPI {
			return fmt.Errorf("managed provider %q has unsupported API type %q", provider.Id, provider.APIType)
		}
	case contracts.DeepSeek:
		if name != string(provider.Id) {
			return fmt.Errorf("managed provider %q must use its provider ID as its name", provider.Id)
		}
		if provider.BaseEndpoint == "" || len(provider.Models) == 0 {
			return fmt.Errorf("managed provider %q has incomplete catalog configuration", provider.Id)
		}
		if provider.APIType != contracts.OpenAICompatableAPI {
			return fmt.Errorf("managed provider %q has unsupported API type %q", provider.Id, provider.APIType)
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
