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

// ProvisionOption carries state an adaptor needs but cannot derive. Only the
// claude-cli adaptor currently uses one.
type ProvisionOption func(*provisionOptions)

type provisionOptions struct {
	claudeConversation ClaudeConversationStore
}

// WithClaudeConversation hands the claude-cli adaptor a place to keep the CLI
// conversation it is appending to. SetModel and SetThinkingPattern build a new
// adaptor, and without this the CLI session is recreated — one failed start and
// a full-history re-send — on every model switch. Other providers ignore it.
func WithClaudeConversation(store ClaudeConversationStore) ProvisionOption {
	return func(o *provisionOptions) { o.claudeConversation = store }
}

func ProvisionModelClient(
	m contracts.ProvisionedModel,
	workspace string,
	sessionID uuid.UUID,
	opts ...ProvisionOption,
) (contracts.InfaiModelAdaptor, error) {
	var options provisionOptions
	for _, opt := range opts {
		opt(&options)
	}
	switch m.ProviderSlug() {
	case contracts.DeepSeek:
		return NewDeepSeekAPI(m)
	case contracts.Codex:
		return NewOpenAICodexResponsesAPI(m)
	case contracts.ClaudeCLI:
		return NewClaudeCLI(m, workspace, sessionID, options.claudeConversation)
	case contracts.OpenAIGeneric:
		return NewOpenAICompatableAPI(m)
	default:
		return nil, fmt.Errorf("unsupported provider %q", m.ProviderSlug())
	}
}
