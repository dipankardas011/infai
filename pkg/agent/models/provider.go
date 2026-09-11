package models

import (
	"context"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func ProviderAuthMethods(providerID contracts.ProviderSlug) ([]contracts.ProviderAuthMethod, error) {
	switch providerID {
	case contracts.Codex:
		return codexAuthMethods(), nil
	case contracts.DeepSeek:
		return deepSeekAuthMethods(), nil
	default:
		return nil, fmt.Errorf("provider %q does not support managed authentication", providerID)
	}
}

func ProvisionProviderAuth(ctx context.Context, providerID contracts.ProviderSlug, method contracts.LLMProviderAuthMethod, credential string) (contracts.ProviderAuthFlow, error) {
	switch providerID {
	case contracts.Codex:
		return beginCodexAuth(ctx, method)
	case contracts.DeepSeek:
		return beginDeepSeekAuth(method, credential)
	default:
		return nil, fmt.Errorf("provider %q does not support managed authentication", providerID)
	}
}

func RefreshProviderAuth(ctx context.Context, providerID contracts.ProviderSlug, auth contracts.LLMProviderAuth) (contracts.LLMProviderAuth, bool, error) {
	switch providerID {
	case contracts.Codex:
		return refreshCodexAuth(ctx, auth)
	default:
		return auth, false, nil
	}
}

func ProvisionModelClient(
	m contracts.ProvisionedModel,
) (contracts.InfaiModelAdaptor, error) {
	switch m.APIType() {
	case contracts.OpenAICompatableAPI:
		return NewOpenAICompatableAPI(m)
	case contracts.OpenAICodexResponsesAPI:
		return NewOpenAICodexResponsesAPI(m)
	default:
		return nil, fmt.Errorf("unsupported provider API type %q for %q", m.APIType(), m.ProviderSlug())
	}
}
