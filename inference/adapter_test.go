package inference

import (
	"testing"

	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/runner"
)

type legacyAdapter struct{}

func (legacyAdapter) Kind() model.EngineKind { return "legacy" }

func (legacyAdapter) BuildLaunchSpec(model.InferenceEngine, model.ModelEntry, model.Profile) (runner.LaunchSpec, error) {
	return runner.LaunchSpec{Command: "legacy"}, nil
}

func (legacyAdapter) NewMetricsSource(string, int) MetricsSource { return nil }

func TestBuildAdapterLaunchSpecSupportsLegacyAdapterInterface(t *testing.T) {
	var adapter Adapter = legacyAdapter{}
	spec, err := BuildAdapterLaunchSpec(adapter, model.InferenceEngine{}, model.ModelEntry{}, model.Profile{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "legacy" {
		t.Fatalf("command = %q, want legacy", spec.Command)
	}

	draft := &model.ModelEntry{ID: 2}
	if _, err := BuildAdapterLaunchSpec(adapter, model.InferenceEngine{}, model.ModelEntry{}, model.Profile{}, draft); err == nil {
		t.Fatal("legacy adapter must report unsupported draft models")
	}
}
