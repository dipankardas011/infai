package contracts

import "context"

type ProviderAuthMethod struct {
	Method      LLMProviderAuthMethod `json:"method"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	SecretInput bool                  `json:"secret_input,omitempty"`
}

type ProviderAuthChallenge struct {
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
}

type ProviderAuthStatus string

const (
	ProviderAuthPending  ProviderAuthStatus = "pending"
	ProviderAuthComplete ProviderAuthStatus = "complete"
	ProviderAuthFailed   ProviderAuthStatus = "failed"
)

type ProviderAuthFlow interface {
	Challenge() ProviderAuthChallenge
	Complete(context.Context) (LLMProviderAuth, error)
}
