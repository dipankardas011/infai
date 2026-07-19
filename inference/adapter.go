package inference

import (
	"fmt"

	"github.com/dipankardas011/infai/launcher"
	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/runner"
)

type RunSpec struct {
	Launch  runner.LaunchSpec
	Metrics MetricsSource
}

type Adapter interface {
	Kind() model.EngineKind
	BuildLaunchSpec(model.InferenceEngine, model.ModelEntry, model.Profile) (runner.LaunchSpec, error)
	NewMetricsSource(host string, port int) MetricsSource
}

func AdapterFor(kind model.EngineKind) (Adapter, error) {
	switch kind {
	case "", model.EngineLlamaCPP:
		return llamaCPPAdapter{}, nil
	case model.EngineVLLM:
		return vllmAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported inference engine kind %q", kind)
	}
}

type llamaCPPAdapter struct{}

func (llamaCPPAdapter) Kind() model.EngineKind { return model.EngineLlamaCPP }

func (llamaCPPAdapter) BuildLaunchSpec(engine model.InferenceEngine, entry model.ModelEntry, profile model.Profile) (runner.LaunchSpec, error) {
	return launcher.BuildLlamaCPPSpec(engine, entry, profile)
}

func (llamaCPPAdapter) NewMetricsSource(host string, port int) MetricsSource {
	return newHTTPMetricsSource(model.EngineLlamaCPP, host, port)
}

type vllmAdapter struct{}

func (vllmAdapter) Kind() model.EngineKind { return model.EngineVLLM }

func (vllmAdapter) BuildLaunchSpec(engine model.InferenceEngine, entry model.ModelEntry, profile model.Profile) (runner.LaunchSpec, error) {
	return launcher.BuildVLLMSpec(engine, entry, profile)
}

func (vllmAdapter) NewMetricsSource(host string, port int) MetricsSource {
	return newHTTPMetricsSource(model.EngineVLLM, host, port)
}
