package backend

import (
	"testing"

	"github.com/dipankardas011/infai/internal/model"
)

func TestOptionCatalogIsValid(t *testing.T) {
	if err := ValidateOptionCatalog(); err != nil {
		t.Fatal(err)
	}
}

func TestOptionsForIncludesCommonAndEngineOptions(t *testing.T) {
	llama := OptionsFor(model.EngineLlamaCPP)
	vllm := OptionsFor(model.EngineVLLM)

	contains := func(options []Option, key string) bool {
		for _, option := range options {
			if option.Key == key {
				return true
			}
		}
		return false
	}
	for _, key := range []string{"host", "port", "context", "extra_flags", "gpu_layers", "cache_type_k", "mmproj"} {
		if !contains(llama, key) {
			t.Errorf("llama catalog missing %q", key)
		}
	}
	for _, key := range []string{"host", "port", "context", "extra_flags", "gpu_memory_utilization", "dtype", "trust_remote_code"} {
		if !contains(vllm, key) {
			t.Errorf("vLLM catalog missing %q", key)
		}
	}
	if contains(llama, "trust_remote_code") || contains(vllm, "gpu_layers") {
		t.Fatal("engine-specific options leaked into the wrong catalog")
	}
}

func TestOptionCatalogMapsPersistedFields(t *testing.T) {
	for _, option := range OptionCatalog() {
		if option.Planned {
			continue
		}
		if option.Key == "extra_flags" {
			continue
		}
		if option.ProfileField == "" && option.EngineConfigField == "" {
			t.Errorf("%q has no persisted field", option.Key)
		}
	}
}

func TestOptionCatalogReturnsIndependentChoices(t *testing.T) {
	first := OptionsFor(model.EngineLlamaCPP)
	second := OptionsFor(model.EngineLlamaCPP)
	var firstCache, secondCache *Option
	for i := range first {
		if first[i].Key == "cache_type_k" {
			firstCache = &first[i]
		}
	}
	for i := range second {
		if second[i].Key == "cache_type_k" {
			secondCache = &second[i]
		}
	}
	if firstCache == nil || secondCache == nil {
		t.Fatal("cache option missing")
	}
	firstCache.Choices[0] = "mutated"
	if secondCache.Choices[0] == "mutated" {
		t.Fatal("catalog choices share mutable storage")
	}
}
