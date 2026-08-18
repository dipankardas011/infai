package backend

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/internal/hardware"
	"github.com/dipankardas011/infai/internal/memoryfit"
	"github.com/dipankardas011/infai/internal/model"
)

func TestSuggestProfileLlamaCPP(t *testing.T) {
	req := suggestionRequest(model.EngineLlamaCPP, model.TypeGGUF)
	suggestion, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	if suggestion.Draft.ContextSize != 65536 || suggestion.Fit.Fit == memoryfit.FitUnknown {
		t.Fatalf("unexpected suggestion: %#v", suggestion)
	}
	if suggestion.Draft.NGL != "auto" || suggestion.Draft.BatchSize == nil || *suggestion.Draft.BatchSize != 512 || suggestion.Draft.UBatchSize == nil || *suggestion.Draft.UBatchSize != 128 {
		t.Fatalf("unexpected llama draft: %#v", suggestion.Draft)
	}
	if suggestion.Draft.UseMmproj {
		t.Fatal("projector should not be enabled automatically")
	}
	if len(suggestion.Alternatives) == 0 || len(suggestion.Reasons) == 0 {
		t.Fatalf("missing explanation or alternatives: %#v", suggestion)
	}
}

func TestSuggestProfileVLLM(t *testing.T) {
	req := suggestionRequest(model.EngineVLLM, model.TypeSafetensors)
	suggestion, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.VLLMConfig
	if err := json.Unmarshal([]byte(suggestion.Draft.EngineConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GPUUtilization == nil || *cfg.GPUUtilization != 0.85 || cfg.MaxNumSeqs == nil || *cfg.MaxNumSeqs != 8 || cfg.MaxBatchedTokens == nil || *cfg.MaxBatchedTokens != 4096 || cfg.DType != "auto" {
		t.Fatalf("unexpected vLLM config: %#v", cfg)
	}
	if cfg.TensorParallelSize != nil || cfg.PipelineParallelSize != nil {
		t.Fatal("multi-GPU settings should be omitted")
	}
}

func TestSuggestProfileUsesCombinedMemoryForAutoOffload(t *testing.T) {
	req := suggestionRequest(model.EngineLlamaCPP, model.TypeGGUF)
	req.Hardware.Accelerators[0].FreeVRAMBytes = 1 * 1024 * 1024 * 1024
	suggestion, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	if suggestion.Fit.Fit == memoryfit.FitDoesNotFit {
		t.Fatalf("fit: got %s", suggestion.Fit.Fit)
	}
	if !strings.Contains(strings.Join(suggestion.Fit.Assumptions, " "), "combined VRAM and available RAM") {
		t.Fatalf("missing combined-memory assumption: %#v", suggestion.Fit.Assumptions)
	}
	if suggestion.Draft.ContextSize != 65536 {
		t.Fatalf("unexpected suggested context: %d", suggestion.Draft.ContextSize)
	}
}

func TestSuggestProfileUnknownHardwareReturnsSentinel(t *testing.T) {
	req := suggestionRequest(model.EngineLlamaCPP, model.TypeGGUF)
	req.Hardware.Accelerators = append(req.Hardware.Accelerators, req.Hardware.Accelerators[0])
	_, err := SuggestProfile(req)
	if !errors.Is(err, memoryfit.ErrCannotEstimate) {
		t.Fatalf("error %v is not ErrCannotEstimate", err)
	}
}

func TestSuggestProfileIsDeterministic(t *testing.T) {
	req := suggestionRequest(model.EngineVLLM, model.TypeSafetensors)
	first, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("suggestions differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestSuggestProfileUsesHybridKVMetadata(t *testing.T) {
	req := suggestionRequest(model.EngineLlamaCPP, model.TypeGGUF)
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(req.Model.Metadata), &meta); err != nil {
		t.Fatal(err)
	}
	meta.ContextLength = 131072
	meta.BlockCount = 35
	meta.AttentionHeadCountKV = 1
	meta.HeadDimension = 512
	meta.SlidingWindow = 512
	meta.GlobalAttentionLayers = 7
	meta.KVCacheSharedLayers = 20
	meta.AttentionLayerTypes = make([]string, 35)
	for i := range meta.AttentionLayerTypes {
		meta.AttentionLayerTypes[i] = "sliding_attention"
	}
	for i := 0; i < 3; i++ {
		meta.AttentionLayerTypes[i] = "full_attention"
	}
	req.Model.Metadata = marshalSuggestion(meta)

	suggestion, err := SuggestProfile(req)
	if err != nil {
		t.Fatal(err)
	}
	assumptions := strings.Join(suggestion.Fit.Assumptions, " ")
	if !strings.Contains(assumptions, "3 full-attention layers") || !strings.Contains(assumptions, "12 sliding-window layers") {
		t.Fatalf("suggestion did not use hybrid KV metadata: %q", assumptions)
	}
}

func TestCompatibleInferenceEngines(t *testing.T) {
	engines := []model.InferenceEngine{
		{ID: "llama", Kind: model.EngineLlamaCPP},
		{ID: "vllm", Kind: model.EngineVLLM},
	}
	gguf := CompatibleInferenceEngines(model.ModelEntry{Type: model.TypeGGUF}, engines)
	if len(gguf) != 1 || gguf[0].ID != "llama" {
		t.Fatalf("GGUF engines: %#v", gguf)
	}
	safetensors := CompatibleInferenceEngines(model.ModelEntry{Type: model.TypeSafetensors}, engines)
	if len(safetensors) != 1 || safetensors[0].ID != "vllm" {
		t.Fatalf("SafeTensors engines: %#v", safetensors)
	}
}

func suggestionRequest(kind model.EngineKind, modelType model.ModelType) SuggestionRequest {
	meta := model.ModelMetadata{
		Architecture:         "llama",
		ContextLength:        65536,
		BlockCount:           32,
		AttentionHeadCount:   32,
		AttentionHeadCountKV: 8,
		HeadDimension:        128,
		FileSizeBytes:        4 * 1024 * 1024 * 1024,
		Quantization:         "Q4_K_M",
	}
	engine := model.InferenceEngine{ID: "engine-1", Kind: kind, Name: string(kind)}
	return SuggestionRequest{
		Model:  model.ModelEntry{ID: 1, DisplayName: "Test model", Type: modelType, Metadata: marshalSuggestion(meta)},
		Engine: engine,
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

func marshalSuggestion(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}
