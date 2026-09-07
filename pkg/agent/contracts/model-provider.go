package contracts

import (
	"context"
	"sort"
	"time"
)

const (
	ProviderIDOpenAICodex = "openai-codex"

	AuthTypeNone   = "none"
	AuthTypeBasic  = "basic"
	AuthTypeBearer = "bearer"
	AuthTypeOAuth  = "oauth"
)

// ProviderAuth is the complete persisted authentication state. A provider
// decides which auth types it supports; the engine only coordinates it.
type ProviderAuth struct {
	Type         string    `toml:"type" json:"type"`
	Username     string    `toml:"username,omitempty" json:"username,omitempty"`
	Password     string    `toml:"password,omitempty" json:"password,omitempty"`
	Token        string    `toml:"token,omitempty" json:"token,omitempty"`
	AccessToken  string    `toml:"access_token,omitempty" json:"access_token,omitempty"`
	RefreshToken string    `toml:"refresh_token,omitempty" json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `toml:"expires_at" json:"expires_at"`
	AccountID    string    `toml:"account_id,omitempty" json:"account_id,omitempty"`
}

func (a ProviderAuth) Authenticated() bool {
	switch a.Type {
	case AuthTypeNone:
		return true
	case AuthTypeBasic:
		return a.Username != "" || a.Password != ""
	case AuthTypeBearer:
		return a.Token != ""
	case AuthTypeOAuth:
		return a.AccessToken != "" && a.RefreshToken != "" && a.AccountID != ""
	default:
		return false
	}
}

func (a ProviderAuth) Empty() bool {
	return a.Type == "" && a.Username == "" && a.Password == "" && a.Token == "" &&
		a.AccessToken == "" && a.RefreshToken == "" && a.ExpiresAt.IsZero() && a.AccountID == ""
}

type ProviderModel struct {
	Name             string            `toml:"name,omitempty" json:"name,omitempty"`
	DisplayName      string            `toml:"display_name,omitempty" json:"display_name,omitempty"`
	ContextWindow    int               `toml:"context_length" json:"context_length"`
	Input            []string          `toml:"input,omitempty" json:"input,omitempty"`
	Reasoning        bool              `toml:"reasoning,omitempty" json:"reasoning,omitempty"`
	ThinkingModes    []string          `toml:"thinking_modes,omitempty" json:"thinking_modes,omitempty"`
	ThinkingLevelMap map[string]string `toml:"thinking_level_map,omitempty" json:"thinking_level_map,omitempty"`
	ReasoningEffort  string            `toml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
}

func (m ProviderModel) EffectiveName(key string) string {
	if m.Name != "" {
		return m.Name
	}
	return key
}

type ProviderConfig struct {
	ID           string                   `toml:"-" json:"id"`
	BaseEndpoint string                   `toml:"base_endpoint,omitempty" json:"base_endpoint,omitempty"`
	Auth         ProviderAuth             `toml:"-" json:"-"`
	Models       map[string]ProviderModel `toml:"models,omitempty" json:"models,omitempty"`
}

func (p ProviderConfig) Authenticated() bool { return p.Auth.Authenticated() }

func (p ProviderConfig) ModelNames() []string {
	names := make([]string, 0, len(p.Models))
	for name := range p.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p ProviderConfig) Model(name string) (ProviderModel, bool) {
	if model, ok := p.Models[name]; ok {
		model.Name = model.EffectiveName(name)
		return model, true
	}
	for key, model := range p.Models {
		if model.EffectiveName(key) == name {
			model.Name = name
			return model, true
		}
	}
	return ProviderModel{}, false
}

// ProviderStateStore is the persistence boundary available to providers.
// UpdateProvider atomically persists authentication and model catalog changes.
type ProviderStateStore interface {
	Get(string) (ProviderConfig, bool)
	UpdateProvider(ProviderConfig) error
}

// LLMProvider owns one configured provider's auth, catalog, and model clients.
// Implementations persist their own state through ProviderStateStore.
type LLMProvider interface {
	ID() string
	Config() ProviderConfig
	Login(context.Context, ProviderLoginRequest) error
	Logout() error
	RefreshModels(context.Context) error
	NewModel(modelName, sessionID string) (InfaiModelAdaptor, error)
}

type ProviderLoginRequest struct {
	Method   string
	Username string
	Password string
	Token    string
	Notify   func(ProviderLoginEvent) error
}

type ProviderLoginEvent struct {
	URL       string
	UserCode  string
	ExpiresIn time.Duration
}
