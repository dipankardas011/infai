package memoryfit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/hardware"
	"github.com/dipankardas011/infai/model"
)

func TestEstimateDenseGGUFOnGPU(t *testing.T) {
	req := baseRequest()
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fit != FitFits || result.Confidence != ConfidenceHigh {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Breakdown.WeightsBytes != 4*1024*1024*1024 {
		t.Fatalf("weights: got %d", result.Breakdown.WeightsBytes)
	}
	wantKV := uint64(2 * 32 * 8 * 128 * 4096 * 2)
	if result.Breakdown.KVCacheBytes != wantKV {
		t.Fatalf("KV cache: got %d want %d", result.Breakdown.KVCacheBytes, wantKV)
	}
	if result.Breakdown.RuntimeOverheadBytes != DefaultRuntimeOverhead {
		t.Fatalf("runtime overhead: got %d", result.Breakdown.RuntimeOverheadBytes)
	}
	if result.Breakdown.SafetyHeadroomBytes == 0 || result.UsableContext != 4096 {
		t.Fatalf("unexpected safety/context: %#v", result)
	}
}

func TestEstimateCapsContextAtModelLimit(t *testing.T) {
	req := baseRequest()
	req.Profile.ContextSize = 32768
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsableContext != 8192 {
		t.Fatalf("context: got %d", result.UsableContext)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "capped") {
		t.Fatalf("expected context warning: %#v", result.Warnings)
	}
}

func TestEstimateClassifiesTightAndDoesNotFit(t *testing.T) {
	for _, test := range []struct {
		name string
		free uint64
		want FitLevel
	}{
		{name: "tight", free: 6 * 1024 * 1024 * 1024, want: FitTight},
		{name: "does not fit", free: 5 * 1024 * 1024 * 1024, want: FitDoesNotFit},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := baseRequest()
			req.Hardware.Accelerators[0].FreeVRAMBytes = test.free
			result, err := Estimate(req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Fit != test.want {
				t.Fatalf("fit: got %s want %s; result=%#v", result.Fit, test.want, result)
			}
		})
	}
}

func TestEstimateUsesParameterFallbackWhenArtifactSizeMissing(t *testing.T) {
	req := baseRequest()
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(req.Model.Metadata), &meta); err != nil {
		t.Fatal(err)
	}
	meta.FileSizeBytes = 0
	meta.ParameterCount = 8_000_000_000
	meta.Quantization = "Q4_K_M"
	req.Model.Metadata = marshalMetadata(meta)
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Confidence != ConfidenceMedium || len(result.Warnings) == 0 {
		t.Fatalf("expected fallback warning: %#v", result)
	}
	if result.Breakdown.WeightsBytes != 4_000_000_000 {
		t.Fatalf("weights: got %d", result.Breakdown.WeightsBytes)
	}
}

func TestEstimateCPUOnlyLlamaCPP(t *testing.T) {
	req := baseRequest()
	req.Hardware.Accelerators = nil
	req.Hardware.AcceleratorCount = 0
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.PoolAvailableBytes != req.Hardware.RAM.AvailableBytes {
		t.Fatalf("pool available: got %d", result.PoolAvailableBytes)
	}
	if !strings.Contains(strings.Join(result.Assumptions, " "), "CPU-only") {
		t.Fatalf("missing CPU assumption: %#v", result.Assumptions)
	}
}

func TestEstimateVLLMUsesConfiguredGPUUtilization(t *testing.T) {
	req := baseRequest()
	req.Model.Type = model.TypeSafetensors
	req.Engine = model.EngineVLLM
	utilization := 0.5
	req.Profile.EngineConfig = marshal(model.VLLMConfig{GPUUtilization: &utilization})
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(float64(req.Hardware.Accelerators[0].TotalVRAMBytes) * utilization)
	if result.PoolAvailableBytes != want {
		t.Fatalf("pool available: got %d want %d", result.PoolAvailableBytes, want)
	}
	if result.Breakdown.EngineBudgetBytes != want {
		t.Fatalf("engine budget: got %d want %d", result.Breakdown.EngineBudgetBytes, want)
	}
}

func TestEstimateMoEReportsResidentWeightAssumption(t *testing.T) {
	req := baseRequest()
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(req.Model.Metadata), &meta); err != nil {
		t.Fatal(err)
	}
	meta.NumExperts = 8
	meta.NumExpertsPerToken = 2
	req.Model.Metadata = marshalMetadata(meta)
	result, err := Estimate(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Assumptions, " "), "fully resident") {
		t.Fatalf("missing MoE assumption: %#v", result.Assumptions)
	}
}

func TestEstimateReturnsIdentifiableUnknownErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Request)
	}{
		{name: "missing metadata", edit: func(req *Request) { req.Model.Metadata = "" }},
		{name: "multiple GPUs", edit: func(req *Request) {
			req.Hardware.Accelerators = append(req.Hardware.Accelerators, req.Hardware.Accelerators[0])
		}},
		{name: "missing KV architecture", edit: func(req *Request) {
			var meta model.ModelMetadata
			_ = json.Unmarshal([]byte(req.Model.Metadata), &meta)
			meta.HeadDimension = 0
			req.Model.Metadata = marshalMetadata(meta)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := baseRequest()
			test.edit(&req)
			_, err := Estimate(req)
			if !errors.Is(err, ErrCannotEstimate) {
				t.Fatalf("error %v is not ErrCannotEstimate", err)
			}
		})
	}
}

func baseRequest() Request {
	meta := model.ModelMetadata{
		Architecture:         "llama",
		ContextLength:        8192,
		BlockCount:           32,
		AttentionHeadCountKV: 8,
		HeadDimension:        128,
		FileSizeBytes:        4 * 1024 * 1024 * 1024,
		Quantization:         "Q4_K_M",
	}
	return Request{
		Model:   model.ModelEntry{Type: model.TypeGGUF, Metadata: marshalMetadata(meta)},
		Engine:  model.EngineLlamaCPP,
		Profile: model.Profile{ContextSize: 4096},
		Hardware: hardware.Snapshot{
			RAM: hardware.Memory{TotalBytes: 32 * 1024 * 1024 * 1024, AvailableBytes: 24 * 1024 * 1024 * 1024},
			Accelerators: []hardware.Accelerator{{
				Name:           "test GPU",
				Backend:        hardware.BackendNVIDIA,
				TotalVRAMBytes: 16 * 1024 * 1024 * 1024,
				FreeVRAMBytes:  16 * 1024 * 1024 * 1024,
			}},
		},
	}
}

func marshalMetadata(meta model.ModelMetadata) string {
	return marshal(meta)
}

func marshal(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}
