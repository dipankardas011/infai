package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type infaiManagedProviderConfigs map[string]infaiManagedProviderConfig

type infaiManagedProviderConfig struct {
	Provider contracts.ProviderSlug    `json:"provider"`
	APIType  contracts.ProviderAPIType `json:"api_type"`
	BaseURL  string                    `json:"base_url"`
	contracts.LLMModelConfiguration
}

func GetAllInfaiManagedProviderConfigs(ctx context.Context, providerIDs []contracts.ProviderSlug) (map[contracts.ProviderSlug]contracts.LLMProviderConfiguration, error) {
	if slices.Contains(providerIDs, contracts.OpenAIGeneric) {
		return nil, fmt.Errorf("the contract is wrong the providerIds cannot contain openaigeneric providerId")
	}

	type accumulateProvider struct {
		V infaiManagedProviderConfigs
		E error
	}

	ch := make(chan accumulateProvider, len(providerIDs))

	for _, providerID := range providerIDs {
		go func() {
			v, err := getInfaiManagedProviderConfig(ctx, providerID)
			ch <- accumulateProvider{
				V: v,
				E: err,
			}
		}()
	}

	var errJ error

	ret := make(map[contracts.ProviderSlug]contracts.LLMProviderConfiguration, len(providerIDs))

	for range providerIDs {
		r := <-ch

		if r.E != nil {
			errJ = errors.Join(errJ, r.E)
			continue
		}

		for modelID, v := range r.V {
			if providerConfig, ok := ret[v.Provider]; ok {
				providerConfig.Models[modelID] = v.LLMModelConfiguration
				ret[v.Provider] = providerConfig
			} else {
				ret[v.Provider] = contracts.LLMProviderConfiguration{
					BaseEndpoint: v.BaseURL,
					APIType:      v.APIType,
					Id:           v.Provider,
					Models: map[string]contracts.LLMModelConfiguration{
						modelID: v.LLMModelConfiguration,
					},
					Auth: contracts.LLMProviderAuth{},
				}
			}
		}
	}

	return ret, errJ
}

func getInfaiManagedProviderConfig(ctx context.Context, providerID contracts.ProviderSlug) (infaiManagedProviderConfigs, error) {
	const baseURL = "https://infai.dipankar-das.com"

	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}

	endpoint = endpoint.JoinPath(
		"v1",
		"providers",
		string(providerID)+".json",
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"failed to get provider config: %s, for endpoint: %s",
			resp.Status,
			endpoint.String(),
		)
	}

	ret := make(infaiManagedProviderConfigs)

	if err := json.NewDecoder(resp.Body).Decode(&ret); err != nil {
		return nil, err
	}

	return ret, nil
}
