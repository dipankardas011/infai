package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/store"
)

const providerMetadataURL = "https://infai.dipankar-das.com/v1/providers.json"

type providerMetadataDocument struct {
	Providers map[string]providerMetadata `json:"providers"`
}

type providerMetadata struct {
	ID           string                           `json:"id"`
	Name         string                           `json:"name"`
	BaseEndpoint string                           `json:"base_endpoint"`
	API          string                           `json:"api"`
	Models       map[string]providerMetadataModel `json:"models"`
}

type providerMetadataModel struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	ContextLength    int                `json:"context_length"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
	Input            []string           `json:"input"`
	Reasoning        bool               `json:"reasoning"`
}

type ProviderCatalogEntry struct {
	ID           string
	Name         string
	BaseEndpoint string
	API          string
}

func ListProviderCatalog(ctx context.Context) ([]ProviderCatalogEntry, error) {
	document, err := fetchProviderMetadata(ctx, http.DefaultClient, providerMetadataURL)
	if err != nil {
		return nil, err
	}
	entries := make([]ProviderCatalogEntry, 0, len(document.Providers))
	for _, provider := range document.Providers {
		entries = append(entries, ProviderCatalogEntry{
			ID: provider.ID, Name: provider.Name, BaseEndpoint: provider.BaseEndpoint, API: provider.API,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries, nil
}

func fetchProviderMetadata(ctx context.Context, client *http.Client, endpoint string) (providerMetadataDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerMetadataDocument{}, fmt.Errorf("provider metadata: create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerMetadataDocument{}, fmt.Errorf("provider metadata: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return providerMetadataDocument{}, fmt.Errorf("provider metadata: status %d: %s", resp.StatusCode, sanitizeBody(body))
	}
	var document providerMetadataDocument
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&document); err != nil {
		return providerMetadataDocument{}, fmt.Errorf("provider metadata: decode: %w", err)
	}
	if len(document.Providers) == 0 {
		return providerMetadataDocument{}, fmt.Errorf("provider metadata: incomplete document")
	}
	for key, provider := range document.Providers {
		if provider.ID == "" || provider.BaseEndpoint == "" || provider.API == "" || len(provider.Models) == 0 {
			return providerMetadataDocument{}, fmt.Errorf("provider metadata: invalid provider %q", key)
		}
	}
	return document, nil
}

func (d providerMetadataDocument) provider(id, endpoint string) (providerMetadata, bool) {
	if id != "" {
		metadata, ok := d.Providers[id]
		if ok {
			return metadata, true
		}
		if endpoint == "" {
			return providerMetadata{}, false
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return providerMetadata{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, metadata := range d.Providers {
		candidate, err := url.Parse(metadata.BaseEndpoint)
		if err == nil && strings.EqualFold(host, candidate.Hostname()) {
			return metadata, true
		}
	}
	return providerMetadata{}, false
}

func (p providerMetadata) model(slug string) (store.Model, bool) {
	metadata, ok := p.Models[slug]
	if !ok {
		for _, candidate := range p.Models {
			if candidate.ID == slug {
				metadata, ok = candidate, true
				break
			}
		}
	}
	if !ok || metadata.ID == "" || metadata.ContextLength <= 0 {
		return store.Model{}, false
	}
	model := store.Model{
		Name:             metadata.ID,
		DisplayName:      metadata.Name,
		ContextWindow:    metadata.ContextLength,
		Input:            append([]string(nil), metadata.Input...),
		Reasoning:        metadata.Reasoning,
		ThinkingLevelMap: make(map[string]string),
	}
	if !metadata.Reasoning {
		return model, true
	}

	order := []string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent"}
	seen := make(map[string]bool)
	for _, mode := range order {
		if value, exists := metadata.ThinkingLevelMap[mode]; exists && value != nil {
			model.ThinkingModes = append(model.ThinkingModes, mode)
			model.ThinkingLevelMap[mode] = *value
			seen[mode] = true
		}
	}
	var custom []string
	for mode, value := range metadata.ThinkingLevelMap {
		if value != nil && !seen[mode] {
			custom = append(custom, mode)
			model.ThinkingLevelMap[mode] = *value
		}
	}
	sort.Strings(custom)
	model.ThinkingModes = append(model.ThinkingModes, custom...)
	return model, true
}
