package models

import (
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func ProvisionModelClient(
	m contracts.ProvisionedModel,
) (contracts.InfaiModelAdaptor, error) {
	switch m.ProviderSlug() {
	case contracts.OpenAIGeneric:
		return NewOpenAICompatableAPI(m)
	case contracts.Codex:
		panic("not implemented")
	case contracts.DeepSeek:
		panic("not implemented")
	default:
		return nil, fmt.Errorf("unsupported provider slug: %s", m.ProviderSlug())
	}
}
