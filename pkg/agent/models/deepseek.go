package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func deepSeekAuthMethods() []contracts.ProviderAuthMethod {
	return []contracts.ProviderAuthMethod{{
		Method:      contracts.APIKey,
		Name:        "API key",
		Description: "Authenticate with a DeepSeek API key",
		SecretInput: true,
	}}
}

func beginDeepSeekAuth(method contracts.LLMProviderAuthMethod, credential string) (contracts.ProviderAuthFlow, error) {
	if method != contracts.APIKey {
		return nil, fmt.Errorf("deepseek does not support auth method %q", method)
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return nil, errors.New("API key is required")
	}
	return staticAuthFlow{auth: contracts.LLMProviderAuth{Method: contracts.APIKey, BearerToken: credential}}, nil
}

type staticAuthFlow struct {
	auth contracts.LLMProviderAuth
}

func (staticAuthFlow) Challenge() contracts.ProviderAuthChallenge {
	return contracts.ProviderAuthChallenge{}
}

func (f staticAuthFlow) Complete(context.Context) (contracts.LLMProviderAuth, error) {
	return f.auth, nil
}
