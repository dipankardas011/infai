package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Model describes a single model a provider serves, keyed by Name in the
// provider's Models map. Name is the exact id sent to the provider's API; the
// map key is what the UI shows, and when a file entry omits Name it falls back
// to the key.
type Model struct {
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window"`
}

// Provider describes an inference backend the harness can talk to. Name is the
// map key in models.json and is filled from it on load. APIType is "openai"
// (the default) or "anthropic".
type Provider struct {
	Name     string           `json:"name"`
	Endpoint string           `json:"endpoint"`
	APIType  string           `json:"api_type,omitempty"`
	APIKey   string           `json:"api_key,omitempty"`
	Models   map[string]Model `json:"models,omitempty"`
}

// ModelNames returns the provider's model keys, sorted.
func (p Provider) ModelNames() []string {
	names := make([]string, 0, len(p.Models))
	for name := range p.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Model returns the model for a key (or its effective API name), with Name
// filled in when the file left it empty.
func (p Provider) Model(name string) (Model, bool) {
	if m, ok := p.Models[name]; ok {
		if m.Name == "" {
			m.Name = name
		}
		return m, true
	}
	for _, m := range p.Models {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

// ProviderStore loads the provider registry (models.json) and serves it
// read-only. Providers and models are configured by editing models.json
// directly, so the store never writes.
type ProviderStore struct {
	providers map[string]Provider
}

// OpenProviderStore loads the registry from models.json under the harness
// root. A missing file yields an empty registry.
func OpenProviderStore() (*ProviderStore, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if err := EnsureDir(root); err != nil {
		return nil, err
	}
	return NewProviderStore(root + "/models.json")
}

// NewProviderStore loads a provider registry from an explicit path. Used by
// the harness and by tests that want a sandboxed location.
func NewProviderStore(path string) (*ProviderStore, error) {
	p := &ProviderStore{}
	if err := p.load(path); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *ProviderStore) load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			p.providers = map[string]Provider{}
			return nil
		}
		return fmt.Errorf("store: read models: %w", err)
	}

	var f struct {
		Providers map[string]Provider `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("store: parse models: %w", err)
	}

	providers := make(map[string]Provider, len(f.Providers))
	for name, prov := range f.Providers {
		prov.Name = name
		if prov.APIType == "" {
			prov.APIType = "openai"
		}
		providers[name] = prov
	}
	p.providers = providers
	return nil
}

func sortedNames(provs map[string]Provider) []string {
	names := make([]string, 0, len(provs))
	for name := range provs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// List returns every configured provider, sorted by name.
func (p *ProviderStore) List() []Provider {
	names := sortedNames(p.providers)
	out := make([]Provider, 0, len(names))
	for _, name := range names {
		out = append(out, p.providers[name])
	}
	return out
}

// Get returns the named provider.
func (p *ProviderStore) Get(name string) (Provider, bool) {
	prov, ok := p.providers[name]
	return prov, ok
}
