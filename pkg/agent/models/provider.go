package models

import (
	"context"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

func ProviderAuthMethods(providerID contracts.ProviderSlug) ([]contracts.ProviderAuthMethod, error) {
	switch providerID {
	case contracts.Codex:
		return codexAuthMethods(), nil
	case contracts.DeepSeek:
		return deepSeekAuthMethods(), nil
	case contracts.ClaudeCLI:
		return claudeCLIAuthMethods(), nil
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
	case contracts.ClaudeCLI:
		return beginClaudeCLIAuth(ctx, method)
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

// ProvisionOption configures provider-specific behaviour at model construction.
type ProvisionOption func(*provisionOpts)

type provisionOpts struct {
	claudeCLIMeta *contracts.ClaudeCLIMeta
}

// WithSessionClaudeCLIMeta passes persisted resume state to the Claude CLI.
func WithSessionClaudeCLIMeta(meta *contracts.ClaudeCLIMeta) ProvisionOption {
	return func(o *provisionOpts) { o.claudeCLIMeta = meta }
}

func ProvisionModelClient(
	m contracts.ProvisionedModel,
	workspace string,
	sessionID uuid.UUID,
	opts ...ProvisionOption,
) (contracts.InfaiModelAdaptor, error) {
	o := &provisionOpts{}
	for _, opt := range opts {
		opt(o)
	}
	switch m.ProviderSlug() {
	case contracts.DeepSeek:
		return NewDeepSeekAPI(m)
	case contracts.Codex:
		return NewOpenAICodexResponsesAPI(m)
	case contracts.ClaudeCLI:
		var cliOpts []ClaudeCLIOption
		if o.claudeCLIMeta != nil {
			cliOpts = append(cliOpts, WithClaudeCLIMeta(o.claudeCLIMeta))
		}
		return NewClaudeCLI(m, workspace, sessionID, cliOpts...)
	case contracts.OpenAIGeneric:
		return NewOpenAICompatableAPI(m)
	default:
		return nil, fmt.Errorf("unsupported provider %q", m.ProviderSlug())
	}
}
