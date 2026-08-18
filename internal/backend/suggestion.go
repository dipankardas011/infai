package backend

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dipankardas011/infai/internal/hardware"
	"github.com/dipankardas011/infai/internal/memoryfit"
	"github.com/dipankardas011/infai/internal/model"
)

const (
	SuggestionPort      = 8000
	SuggestionHost      = "0.0.0.0"
	SuggestionBatch     = 512
	SuggestionUBatch    = 128
	SuggestionVLLMUtil  = 0.85
	SuggestionVLLMSeqs  = 8
	SuggestionVLLMBatch = 4096
)

type SuggestionRequest struct {
	Model    model.ModelEntry
	Engine   model.InferenceEngine
	Hardware hardware.Snapshot
	Policy   memoryfit.Policy
}

type ProfileSuggestion struct {
	Draft         model.Profile
	NativeContext int
	Fit           memoryfit.Result
	Reasons       []string
	Warnings      []string
	Alternatives  []ProfileAlternative
}

type ProfileAlternative struct {
	Draft    model.Profile
	Fit      memoryfit.Result
	Reasons  []string
	Warnings []string
}

// CompatibleInferenceEngines filters engines that can serve the model type.
func CompatibleInferenceEngines(entry model.ModelEntry, engines []model.InferenceEngine) []model.InferenceEngine {
	want := model.EngineKind("")
	switch entry.Type {
	case model.TypeGGUF, model.TypeGGUFMultimodal:
		want = model.EngineLlamaCPP
	case model.TypeSafetensors, model.TypeHFQuantized:
		want = model.EngineVLLM
	default:
		return nil
	}
	compatible := make([]model.InferenceEngine, 0, len(engines))
	for _, engine := range engines {
		kind := engine.Kind
		if kind == "" {
			kind = model.EngineLlamaCPP
		}
		if kind == want {
			compatible = append(compatible, engine)
		}
	}
	return compatible
}

func SuggestProfile(req SuggestionRequest) (ProfileSuggestion, error) {
	engineKind := req.Engine.Kind
	if engineKind == "" {
		engineKind = model.EngineLlamaCPP
	}
	if engineKind != model.EngineLlamaCPP && engineKind != model.EngineVLLM {
		return ProfileSuggestion{}, fmt.Errorf("unsupported inference engine kind %q", engineKind)
	}
	engine := req.Engine
	engine.Kind = engineKind

	contexts, err := suggestionContexts(req.Model)
	if err != nil {
		return ProfileSuggestion{}, err
	}

	evaluated := make([]evaluatedSuggestion, 0, len(contexts))
	var warnings []string
	for _, contextSize := range contexts {
		draft, err := suggestionDraft(req, engineKind, contextSize)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not build %d-token alternative: %v", contextSize, err))
			continue
		}
		alternative, err := evaluateSuggestion(req, draft, engine, engineKind, contextSize)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not estimate %d-token alternative: %v", contextSize, err))
			continue
		}
		evaluated = append(evaluated, alternative)
	}
	if len(evaluated) == 0 {
		return ProfileSuggestion{}, fmt.Errorf("%w: no context candidates could be evaluated", memoryfit.ErrCannotEstimate)
	}
	selected := -1
	for i := range evaluated {
		if evaluated[i].Fit.Fit != memoryfit.FitDoesNotFit && (selected < 0 || evaluated[i].Draft.ContextSize > evaluated[selected].Draft.ContextSize) {
			selected = i
		}
	}
	if selected < 0 {
		// Nothing fits. Keep the least demanding candidate as the draft rather
		// than opening the editor with the model's maximum context.
		selected = len(evaluated) - 1
	}
	result := ProfileSuggestion{
		Draft:         evaluated[selected].Draft,
		NativeContext: contexts[0],
		Fit:           evaluated[selected].Fit,
		Reasons:       evaluated[selected].Reasons,
		Warnings:      append(append([]string(nil), evaluated[selected].Warnings...), warnings...),
	}
	for i, candidate := range evaluated {
		if i == selected {
			continue
		}
		result.Alternatives = append(result.Alternatives, ProfileAlternative{
			Draft:    candidate.Draft,
			Fit:      candidate.Fit,
			Reasons:  candidate.Reasons,
			Warnings: candidate.Warnings,
		})
	}

	return result, nil
}

type evaluatedSuggestion struct {
	Draft    model.Profile
	Fit      memoryfit.Result
	Reasons  []string
	Warnings []string
}

func evaluateSuggestion(req SuggestionRequest, draft model.Profile, engine model.InferenceEngine, engineKind model.EngineKind, contextSize int) (evaluatedSuggestion, error) {
	fit, err := memoryfit.Estimate(memoryfit.Request{
		Model:    req.Model,
		Engine:   engineKind,
		Profile:  draft,
		Hardware: req.Hardware,
		Policy:   req.Policy,
	})
	if err != nil {
		return evaluatedSuggestion{}, err
	}

	issues := ValidateProfile(&draft, &req.Model, &engine)
	if issues.HasErrors() {
		return evaluatedSuggestion{}, fmt.Errorf("suggested profile validation: %w", issues.Errors())
	}
	warnings := append([]string(nil), fit.Warnings...)
	for _, issue := range issues.Warnings() {
		warnings = append(warnings, issue.Error())
	}
	if engineKind == model.EngineLlamaCPP {
		warnings = append(warnings, "exact batch workspace is backend/build dependent and is not included in the static memory estimate")
	} else {
		warnings = append(warnings, "exact vLLM scheduler workspace depends on the installed engine and is represented by the engine budget")
	}
	reasons := suggestionReasons(engineKind, contextSize, req.Model.MmprojPath != "")
	if fit.Fit == memoryfit.FitDoesNotFit {
		warnings = append(warnings, fmt.Sprintf("evaluated configuration does not fit: context=%d, required=%d bytes, available=%d bytes", contextSize, fit.Breakdown.RequiredBytes, fit.PoolAvailableBytes))
	}
	return evaluatedSuggestion{Draft: draft, Fit: fit, Reasons: reasons, Warnings: warnings}, nil
}

func suggestionDraft(req SuggestionRequest, engine model.EngineKind, contextSize int) (model.Profile, error) {
	draft := model.Profile{
		ModelID:           req.Model.ID,
		InferenceEngineID: req.Engine.ID,
		Name:              suggestionName(req.Model.DisplayName),
		Port:              SuggestionPort,
		Host:              SuggestionHost,
		ContextSize:       contextSize,
	}

	if engine == model.EngineLlamaCPP {
		cacheK, cacheV := "f16", "f16"
		draft.NGL = "auto"
		draft.BatchSize = intPointer(SuggestionBatch)
		draft.UBatchSize = intPointer(SuggestionUBatch)
		draft.CacheTypeK = &cacheK
		draft.CacheTypeV = &cacheV
		draft.UseMmproj = false
		return draft, nil
	}

	utilization := SuggestionVLLMUtil
	config := model.VLLMConfig{
		GPUUtilization:      &utilization,
		MaxNumSeqs:          intPointer(SuggestionVLLMSeqs),
		MaxBatchedTokens:    intPointer(SuggestionVLLMBatch),
		DType:               "auto",
		EnablePrefixCaching: false,
		TrustRemoteCode:     false,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return model.Profile{}, fmt.Errorf("encode vLLM suggestion: %w", err)
	}
	draft.EngineConfig = string(raw)
	return draft, nil
}

func suggestionContexts(entry model.ModelEntry) ([]int, error) {
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(entry.Metadata), &meta); err != nil {
		return nil, fmt.Errorf("%w: invalid model metadata: %v", memoryfit.ErrCannotEstimate, err)
	}
	if meta.ContextLength == 0 {
		return nil, fmt.Errorf("%w: model context length is missing", memoryfit.ErrCannotEstimate)
	}
	maxContext := uint64(meta.ContextLength)
	candidates := []uint64{maxContext}
	power := uint64(1)
	for power <= maxContext/2 {
		power <<= 1
	}
	for power >= 2*1024 {
		candidates = append(candidates, power)
		if power == 2*1024 {
			break
		}
		power >>= 1
	}
	seen := make(map[uint64]bool, len(candidates))
	result := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate > maxContext || candidate <= 0 || seen[candidate] {
			continue
		}
		seen[candidate] = true
		result = append(result, int(candidate))
	}
	return result, nil
}

func suggestionReasons(engine model.EngineKind, contextSize int, hasMmproj bool) []string {
	reasons := []string{fmt.Sprintf("context set to %d tokens based on model capacity and available hardware", contextSize)}
	if engine == model.EngineLlamaCPP {
		reasons = append(reasons,
			"GPU layers set to auto so llama.cpp can choose a safe offload level",
			"f16 KV cache selected as the quality-preserving default",
			"batch 512 and ubatch 128 selected as conservative prompt-processing defaults",
			"exact batch workspace is backend/build dependent and is surfaced as a fit warning rather than estimated precisely",
		)
		if hasMmproj {
			reasons = append(reasons, "multimodal projector remains disabled by default and can be enabled by the user")
		}
	} else {
		reasons = append(reasons,
			"vLLM GPU memory utilization set to 0.85 to preserve runtime headroom",
			"maximum sequences set to 8 for a local-hardware concurrency default",
			"maximum batched tokens set to 4096 for a conservative vLLM profile",
			"tensor and pipeline parallelism omitted because multi-GPU placement is not supported",
			"vLLM engine budget covers its runtime allocation policy; exact scheduler workspace remains engine dependent",
		)
	}
	return reasons
}

func suggestionName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return "Suggested profile"
	}
	return displayName + " suggested"
}

func intPointer(value int) *int {
	return &value
}
