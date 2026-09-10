package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type providerStore struct {
	Providers map[string]storageProvider `json:"providers"`
}

type storageProvider struct {
	Id   contracts.ProviderSlug    `json:"id"`
	Auth contracts.LLMProviderAuth `json:"auth"`

	APIType      *contracts.ProviderAPIType                 `json:"api_type,omitempty"`
	BaseEndpoint *string                                    `json:"base_endpoint,omitempty"`
	Models       map[string]contracts.LLMModelConfiguration `json:"models,omitempty"`
}

func readIt() (*providerStore, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}

	if err := EnsureDir(root); err != nil {
		return nil, err
	}

	path := filepath.Join(root, "models.json")

	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("store: read models: %w", err)
		}

		f := &providerStore{
			Providers: make(map[string]storageProvider),
		}

		if err := writeIt(f); err != nil {
			return nil, err
		}

		return f, nil
	}

	var f providerStore

	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("store: parse models: %w", err)
	}

	// Defensive handling for files like:
	// {}
	// or
	// {"providers": null}
	if f.Providers == nil {
		f.Providers = make(map[string]storageProvider)
	}

	return &f, nil
}

func writeIt(f *providerStore) error {
	root, err := Root()
	if err != nil {
		return err
	}

	if err := EnsureDir(root); err != nil {
		return err
	}

	if f.Providers == nil {
		f.Providers = make(map[string]storageProvider)
	}

	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("store: marshal models: %w", err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "models.json"),
		b,
		0600,
	); err != nil {
		return fmt.Errorf("store: write models: %w", err)
	}

	return nil
}

func LoadProviders() (contracts.LLMProviders, error) {
	ret := contracts.LLMProviders{
		Providers: make(map[string]contracts.LLMProviderConfiguration),
	}

	f, err := readIt()
	if err != nil {
		return ret, err
	}

	for providerName, p := range f.Providers {
		v := contracts.LLMProviderConfiguration{
			Id:   p.Id,
			Auth: p.Auth,
		}

		if p.Id == contracts.OpenAIGeneric {
			if p.BaseEndpoint != nil {
				v.BaseEndpoint = *p.BaseEndpoint
			}
			if p.APIType != nil {
				v.APIType = *p.APIType
			}

			v.Models = p.Models
		}

		ret.Providers[providerName] = v
	}

	return ret, nil
}

func PersistProviders(o contracts.LLMProviders) error {
	f := &providerStore{
		Providers: make(map[string]storageProvider),
	}

	for providerName, p := range o.Providers {
		v := storageProvider{
			Id:   p.Id,
			Auth: p.Auth,
		}

		if p.Id == contracts.OpenAIGeneric {
			if p.BaseEndpoint != "" {
				v.BaseEndpoint = &p.BaseEndpoint
			}
			if p.APIType != "" {
				v.APIType = &p.APIType
			}

			if len(p.Models) > 0 {
				v.Models = p.Models
			}
		}

		f.Providers[providerName] = v
	}

	return writeIt(f)
}
