package store

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/pelletier/go-toml/v2"
)

const (
	ProviderIDOpenAICodex = contracts.ProviderIDOpenAICodex
	AuthTypeNone          = contracts.AuthTypeNone
	AuthTypeBasic         = contracts.AuthTypeBasic
	AuthTypeBearer        = contracts.AuthTypeBearer
	AuthTypeOAuth         = contracts.AuthTypeOAuth
)

var (
	ErrProviderNotFound = errors.New("store: provider not found")
	ErrProviderExists   = errors.New("store: provider already exists")
	ErrModelNotFound    = errors.New("store: model not found")
	ErrModelExists      = errors.New("store: model already exists")
	ErrInvalidRegistry  = errors.New("store: invalid provider registry")
)

type Auth = contracts.ProviderAuth
type Model = contracts.ProviderModel
type Provider = contracts.ProviderConfig

// ProviderStore owns the in-memory and on-disk provider registry.
type ProviderStore struct {
	mu        sync.RWMutex
	path      string
	providers map[string]Provider
}

type providerFile struct {
	Providers map[string]providerRecord `toml:"providers"`
}

// A pointer makes an absent auth table distinguishable and keeps empty auth
// blocks out of files written by the store.
type providerRecord struct {
	BaseEndpoint string           `toml:"base_endpoint,omitempty"`
	Auth         *Auth            `toml:"auth,omitempty"`
	Models       map[string]Model `toml:"models,omitempty"`
}

type providerWriteFile struct {
	Providers map[string]providerWriteRecord `toml:"providers"`
}

type providerWriteRecord struct {
	BaseEndpoint string           `toml:"base_endpoint,omitempty"`
	Auth         map[string]any   `toml:"auth,omitempty"`
	Models       map[string]Model `toml:"models,omitempty"`
}

// OpenProviderStore opens models.toml under the harness root.
func OpenProviderStore() (*ProviderStore, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if err := EnsureDir(root); err != nil {
		return nil, err
	}
	return NewProviderStore(filepath.Join(root, "models.toml"))
}

// NewProviderStore loads a registry from path. A missing file is an empty
// in-memory registry and is not created until the first mutation.
func NewProviderStore(path string) (*ProviderStore, error) {
	if err := ensureProviderDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	p := &ProviderStore{path: path, providers: make(map[string]Provider)}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Path returns the registry file path.
func (p *ProviderStore) Path() string { return p.path }

// Reload replaces the in-memory registry with the current on-disk contents.
func (p *ProviderStore) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	providers, exists, err := readProviderFile(p.path)
	if err != nil {
		return err
	}
	if exists {
		if err := os.Chmod(p.path, 0o600); err != nil {
			return fmt.Errorf("store: secure models file: %w", err)
		}
	}
	p.providers = providers
	return nil
}

func readProviderFile(path string) (map[string]Provider, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]Provider), false, nil
		}
		return nil, false, fmt.Errorf("store: read models: %w", err)
	}

	var f providerFile
	decoder := toml.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return nil, true, fmt.Errorf("store: parse models: %w", err)
	}

	providers := make(map[string]Provider, len(f.Providers))
	for name, record := range f.Providers {
		provider := Provider{
			ID:           name,
			BaseEndpoint: record.BaseEndpoint,
			Models:       cloneModels(record.Models),
		}
		if record.Auth != nil {
			provider.Auth = *record.Auth
		}
		providers[name] = provider
	}
	if err := validateProviders(providers); err != nil {
		return nil, true, err
	}
	return providers, true, nil
}

// List returns cloned providers sorted by name.
func (p *ProviderStore) List() []Provider {
	p.mu.RLock()
	defer p.mu.RUnlock()

	names := sortedNames(p.providers)
	out := make([]Provider, 0, len(names))
	for _, name := range names {
		out = append(out, cloneProvider(p.providers[name]))
	}
	return out
}

// Get returns a clone of the named provider.
func (p *ProviderStore) Get(name string) (Provider, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	provider, ok := p.providers[name]
	if !ok {
		return Provider{}, false
	}
	return cloneProvider(provider), true
}

// AddProvider adds provider using provider.ID as its registry key.
func (p *ProviderStore) AddProvider(provider Provider) error {
	return p.mutate(func(providers map[string]Provider) error {
		if _, ok := providers[provider.ID]; ok {
			return fmt.Errorf("%w: %q", ErrProviderExists, provider.ID)
		}
		providers[provider.ID] = cloneProvider(provider)
		return nil
	})
}

// UpdateProvider replaces an existing provider using provider.ID as its key.
func (p *ProviderStore) UpdateProvider(provider Provider) error {
	return p.mutate(func(providers map[string]Provider) error {
		if _, ok := providers[provider.ID]; !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, provider.ID)
		}
		providers[provider.ID] = cloneProvider(provider)
		return nil
	})
}

// RemoveProvider removes a provider and all its models.
func (p *ProviderStore) RemoveProvider(name string) error {
	return p.mutate(func(providers map[string]Provider) error {
		if _, ok := providers[name]; !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, name)
		}
		delete(providers, name)
		return nil
	})
}

// SetAuth replaces a provider's credentials.
func (p *ProviderStore) SetAuth(providerName string, auth Auth) error {
	return p.mutate(func(providers map[string]Provider) error {
		provider, ok := providers[providerName]
		if !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, providerName)
		}
		provider.Auth = auth
		providers[providerName] = provider
		return nil
	})
}

// ClearAuth removes all credentials and authentication metadata.
func (p *ProviderStore) ClearAuth(providerName string) error {
	return p.SetAuth(providerName, Auth{})
}

// AddModel adds a model under modelName.
func (p *ProviderStore) AddModel(providerName, modelName string, model Model) error {
	return p.mutate(func(providers map[string]Provider) error {
		provider, ok := providers[providerName]
		if !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, providerName)
		}
		if _, ok := provider.Models[modelName]; ok {
			return fmt.Errorf("%w: %q in provider %q", ErrModelExists, modelName, providerName)
		}
		if provider.Models == nil {
			provider.Models = make(map[string]Model)
		}
		provider.Models[modelName] = cloneModel(model)
		providers[providerName] = provider
		return nil
	})
}

// UpdateModel replaces an existing model.
func (p *ProviderStore) UpdateModel(providerName, modelName string, model Model) error {
	return p.mutate(func(providers map[string]Provider) error {
		provider, ok := providers[providerName]
		if !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, providerName)
		}
		if _, ok := provider.Models[modelName]; !ok {
			return fmt.Errorf("%w: %q in provider %q", ErrModelNotFound, modelName, providerName)
		}
		provider.Models[modelName] = cloneModel(model)
		providers[providerName] = provider
		return nil
	})
}

// RemoveModel removes a model from a provider.
func (p *ProviderStore) RemoveModel(providerName, modelName string) error {
	return p.mutate(func(providers map[string]Provider) error {
		provider, ok := providers[providerName]
		if !ok {
			return fmt.Errorf("%w: %q", ErrProviderNotFound, providerName)
		}
		if _, ok := provider.Models[modelName]; !ok {
			return fmt.Errorf("%w: %q in provider %q", ErrModelNotFound, modelName, providerName)
		}
		delete(provider.Models, modelName)
		providers[providerName] = provider
		return nil
	})
}

func (p *ProviderStore) mutate(change func(map[string]Provider) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	providers := cloneProviders(p.providers)
	if err := change(providers); err != nil {
		return err
	}
	for name, provider := range providers {
		provider.ID = name
		providers[name] = provider
	}
	if err := validateProviders(providers); err != nil {
		return err
	}
	if err := writeProviderFile(p.path, providers); err != nil {
		return err
	}
	p.providers = providers
	return nil
}

func writeProviderFile(path string, providers map[string]Provider) error {
	dir := filepath.Dir(path)
	if err := ensureProviderDir(dir); err != nil {
		return err
	}

	f := providerWriteFile{Providers: make(map[string]providerWriteRecord, len(providers))}
	for name, provider := range providers {
		record := providerWriteRecord{
			BaseEndpoint: provider.BaseEndpoint,
			Models:       cloneModels(provider.Models),
		}
		if !provider.Auth.Empty() {
			record.Auth = map[string]any{
				"type": provider.Auth.Type,
			}
			setString := func(name, value string) {
				if value != "" {
					record.Auth[name] = value
				}
			}
			setString("username", provider.Auth.Username)
			setString("password", provider.Auth.Password)
			setString("token", provider.Auth.Token)
			setString("access_token", provider.Auth.AccessToken)
			setString("refresh_token", provider.Auth.RefreshToken)
			setString("account_id", provider.Auth.AccountID)
			if !provider.Auth.ExpiresAt.IsZero() {
				record.Auth["expires_at"] = provider.Auth.ExpiresAt
			}
		}
		f.Providers[name] = record
	}

	b, err := toml.Marshal(f)
	if err != nil {
		return fmt.Errorf("store: encode models: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".models-*.toml")
	if err != nil {
		return fmt.Errorf("store: create temporary models file: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("store: secure temporary models file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("store: write temporary models file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("store: sync temporary models file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temporary models file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: replace models file: %w", err)
	}
	keep = true
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("store: secure models file: %w", err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		syncErr := dirFile.Sync()
		closeErr := dirFile.Close()
		if syncErr != nil {
			return fmt.Errorf("store: sync models directory: %w", syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("store: close models directory: %w", closeErr)
		}
	}
	return nil
}

func ensureProviderDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create models directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("store: secure models directory: %w", err)
	}
	return nil
}

func validateProviders(providers map[string]Provider) error {
	for name, provider := range providers {
		if strings.TrimSpace(name) == "" {
			return invalid("provider name must not be empty")
		}
		if provider.ID != "" && provider.ID != name {
			return invalid("provider %q has mismatched runtime ID %q", name, provider.ID)
		}
		if provider.BaseEndpoint != "" {
			endpoint, err := url.Parse(provider.BaseEndpoint)
			if err != nil || !endpoint.IsAbs() || endpoint.Host == "" ||
				(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
				return invalid("provider %q base_endpoint must be an absolute http(s) URL", name)
			}
		}
		if err := validateAuth(name, provider); err != nil {
			return err
		}
		if err := validateModels(name, provider.Models); err != nil {
			return err
		}
	}
	return nil
}

func validateAuth(name string, provider Provider) error {
	auth := provider.Auth
	if auth.Empty() {
		return nil
	}
	if auth.Type != AuthTypeNone && auth.Type != AuthTypeBasic && auth.Type != AuthTypeBearer && auth.Type != AuthTypeOAuth {
		return invalid("provider %q has unknown auth type %q", name, auth.Type)
	}
	if provider.ID == ProviderIDOpenAICodex && auth.Type != AuthTypeOAuth {
		return invalid("provider %q requires OAuth authentication", name)
	}
	if provider.ID != ProviderIDOpenAICodex && auth.Type == AuthTypeOAuth {
		return invalid("provider %q does not support OAuth authentication", name)
	}
	if auth.Type != AuthTypeBasic && (auth.Username != "" || auth.Password != "") {
		return invalid("provider %q non-basic auth contains basic credentials", name)
	}
	if auth.Type != AuthTypeBearer && auth.Token != "" {
		return invalid("provider %q non-bearer auth contains a bearer token", name)
	}
	if auth.Type != AuthTypeOAuth && (auth.AccessToken != "" || auth.RefreshToken != "" || !auth.ExpiresAt.IsZero() || auth.AccountID != "") {
		return invalid("provider %q non-OAuth auth contains OAuth fields", name)
	}
	return nil
}

func validateModels(providerName string, models map[string]Model) error {
	allowedModes := map[string]bool{
		"off": true, "none": true, "minimal": true, "low": true, "medium": true,
		"high": true, "xhigh": true, "max": true, "ultra": true, "persistent": true,
	}
	effectiveNames := make(map[string]string, len(models))
	for key, model := range models {
		if strings.TrimSpace(key) == "" {
			return invalid("provider %q has an empty model name", providerName)
		}
		if model.ContextWindow <= 0 {
			return invalid("model %q in provider %q must have context_length greater than zero", key, providerName)
		}
		seenModes := make(map[string]bool, len(model.ThinkingModes))
		for _, mode := range model.ThinkingModes {
			if !allowedModes[mode] {
				return invalid("model %q in provider %q has invalid thinking mode %q", key, providerName, mode)
			}
			if seenModes[mode] {
				return invalid("model %q in provider %q has duplicate thinking mode %q", key, providerName, mode)
			}
			seenModes[mode] = true
		}
		if model.ReasoningEffort != "" && !seenModes[model.ReasoningEffort] {
			return invalid("model %q in provider %q selects reasoning_effort %q outside thinking_modes", key, providerName, model.ReasoningEffort)
		}
		for mode, value := range model.ThinkingLevelMap {
			if !seenModes[mode] {
				return invalid("model %q in provider %q maps thinking level %q outside thinking_modes", key, providerName, mode)
			}
			if strings.TrimSpace(value) == "" {
				return invalid("model %q in provider %q maps thinking level %q to an empty value", key, providerName, mode)
			}
		}
		effective := model.EffectiveName(key)
		if strings.TrimSpace(effective) == "" {
			return invalid("model %q in provider %q has an empty effective API ID", key, providerName)
		}
		if other, ok := effectiveNames[effective]; ok {
			return invalid("models %q and %q in provider %q share effective API ID %q", other, key, providerName, effective)
		}
		effectiveNames[effective] = key
	}
	for key := range models {
		if other, ok := effectiveNames[key]; ok && other != key {
			return invalid("model key %q in provider %q conflicts with model %q API ID", key, providerName, other)
		}
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRegistry, fmt.Sprintf(format, args...))
}

func sortedNames(providers map[string]Provider) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneProviders(providers map[string]Provider) map[string]Provider {
	out := make(map[string]Provider, len(providers))
	for name, provider := range providers {
		out[name] = cloneProvider(provider)
	}
	return out
}

func cloneProvider(provider Provider) Provider {
	provider.Models = cloneModels(provider.Models)
	return provider
}

func cloneModels(models map[string]Model) map[string]Model {
	if models == nil {
		return nil
	}
	out := make(map[string]Model, len(models))
	for name, model := range models {
		out[name] = cloneModel(model)
	}
	return out
}

func cloneModel(model Model) Model {
	model.Input = append([]string(nil), model.Input...)
	model.ThinkingModes = append([]string(nil), model.ThinkingModes...)
	if model.ThinkingLevelMap != nil {
		model.ThinkingLevelMap = make(map[string]string, len(model.ThinkingLevelMap))
		for level, value := range model.ThinkingLevelMap {
			model.ThinkingLevelMap[level] = value
		}
	}
	return model
}
