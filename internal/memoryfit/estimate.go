package memoryfit

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/dipankardas011/infai/internal/hardware"
	"github.com/dipankardas011/infai/internal/model"
)

const (
	MiB                    = uint64(1024 * 1024)
	DefaultRuntimeOverhead = 512 * MiB
	DefaultSafetyHeadroom  = 0.15
	DefaultFitThreshold    = 0.85
	DefaultVLLMUtilization = 0.90
)

type FitLevel string

const (
	FitFits       FitLevel = "fits"
	FitTight      FitLevel = "tight"
	FitDoesNotFit FitLevel = "does_not_fit"
	FitUnknown    FitLevel = "unknown"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
)

type Policy struct {
	SafetyHeadroom       float64
	RuntimeOverheadBytes uint64
	FitThreshold         float64
}

func DefaultPolicy() Policy {
	return Policy{
		SafetyHeadroom:       DefaultSafetyHeadroom,
		RuntimeOverheadBytes: DefaultRuntimeOverhead,
		FitThreshold:         DefaultFitThreshold,
	}
}

type Request struct {
	Model    model.ModelEntry
	Engine   model.EngineKind
	Profile  model.Profile
	Hardware hardware.Snapshot
	Policy   Policy
}

type Breakdown struct {
	WeightsBytes         uint64
	KVCacheBytes         uint64
	RuntimeOverheadBytes uint64
	SafetyHeadroomBytes  uint64
	RequiredBytes        uint64
	EngineBudgetBytes    uint64
}

type Result struct {
	PoolTotalBytes     uint64
	PoolAvailableBytes uint64
	Utilization        float64
	Fit                FitLevel
	UsableContext      uint32
	Confidence         Confidence
	Breakdown          Breakdown
	Assumptions        []string
	Warnings           []string
}

func Estimate(req Request) (Result, error) {
	policy := req.Policy
	if policy == (Policy{}) {
		policy = DefaultPolicy()
	}
	if policy.SafetyHeadroom < 0 || policy.FitThreshold <= 0 || policy.FitThreshold > 1 || policy.RuntimeOverheadBytes == 0 {
		return Result{}, cannotEstimate("invalid estimation policy")
	}

	meta, err := parseMetadata(req.Model.Metadata)
	if err != nil {
		return Result{}, err
	}
	if err := validateCompatibility(req.Model.Type, req.Engine); err != nil {
		return Result{}, err
	}

	pool, err := selectPool(req)
	if err != nil {
		return Result{}, err
	}

	contextSize, warnings := contextSize(meta, req.Profile.ContextSize)
	if contextSize == 0 {
		return Result{}, cannotEstimate("model context length is missing")
	}
	if meta.BlockCount == 0 || meta.AttentionHeadCountKV == 0 || meta.HeadDimension == 0 {
		return Result{}, cannotEstimate("model KV-cache architecture metadata is incomplete")
	}

	weights, confidence, weightAssumptions, err := weightBytes(meta, req)
	if err != nil {
		return Result{}, err
	}
	assumptions := append([]string(nil), weightAssumptions...)
	kvBytes, kvAssumption, err := kvCacheBytes(meta, req)
	if err != nil {
		return Result{}, err
	}
	assumptions = append(assumptions, kvAssumption)
	if meta.NumExperts > 0 {
		if req.Engine == model.EngineLlamaCPP && strings.EqualFold(strings.TrimSpace(req.Profile.NGL), "auto") && len(req.Hardware.Accelerators) == 1 {
			assumptions = append(assumptions, "MoE weights may be split between VRAM and RAM by llama.cpp NGL=auto; exact expert residency is runtime dependent")
		} else {
			assumptions = append(assumptions, "MoE weights are treated as fully resident; expert offload is not estimated")
		}
	}
	if pool.assumption != "" {
		assumptions = append(assumptions, pool.assumption)
	}

	base := weights + kvBytes + policy.RuntimeOverheadBytes
	headroom := uint64(math.Ceil(float64(base) * policy.SafetyHeadroom))
	required := base + headroom
	utilization := math.Inf(1)
	if pool.available > 0 {
		utilization = float64(required) / float64(pool.available)
	}
	fit := classify(utilization, policy.FitThreshold)
	if fit == FitDoesNotFit {
		warnings = append(warnings, "required memory exceeds the selected memory pool")
	}
	if confidence == ConfidenceMedium {
		warnings = append(warnings, "weight memory uses a quantization-size fallback because artifact size is unavailable")
	}

	return Result{
		PoolTotalBytes:     pool.total,
		PoolAvailableBytes: pool.available,
		Utilization:        utilization,
		Fit:                fit,
		UsableContext:      contextSize,
		Confidence:         confidence,
		Breakdown: Breakdown{
			WeightsBytes:         weights,
			KVCacheBytes:         kvBytes,
			RuntimeOverheadBytes: policy.RuntimeOverheadBytes,
			SafetyHeadroomBytes:  headroom,
			RequiredBytes:        required,
			EngineBudgetBytes:    pool.engineBudget,
		},
		Assumptions: assumptions,
		Warnings:    warnings,
	}, nil
}

type selectedPool struct {
	total        uint64
	available    uint64
	engineBudget uint64
	assumption   string
}

func parseMetadata(raw string) (model.ModelMetadata, error) {
	if strings.TrimSpace(raw) == "" {
		return model.ModelMetadata{}, cannotEstimate("model metadata is missing")
	}
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return model.ModelMetadata{}, cannotEstimate("model metadata is invalid: %v", err)
	}
	return meta, nil
}

func validateCompatibility(kind model.ModelType, engine model.EngineKind) error {
	switch {
	case (kind == model.TypeGGUF || kind == model.TypeGGUFMultimodal) && engine != model.EngineLlamaCPP:
		return cannotEstimate("GGUF models require llama.cpp")
	case (kind == model.TypeSafetensors || kind == model.TypeHFQuantized) && engine != model.EngineVLLM:
		return cannotEstimate("safetensors models require vLLM")
	}
	return nil
}

func selectPool(req Request) (selectedPool, error) {
	if len(req.Hardware.Accelerators) > 1 {
		return selectedPool{}, cannotEstimate("multiple accelerators require an explicit placement plan")
	}
	if len(req.Hardware.Accelerators) == 1 {
		gpu := req.Hardware.Accelerators[0]
		if gpu.UnifiedMemory {
			if req.Hardware.RAM.TotalBytes == 0 || req.Hardware.RAM.AvailableBytes == 0 {
				return selectedPool{}, cannotEstimate("unified memory capacity is unavailable")
			}
			return selectedPool{total: req.Hardware.RAM.TotalBytes, available: req.Hardware.RAM.AvailableBytes, assumption: "unified memory is modeled as one shared RAM and VRAM pool"}, nil
		}
		if gpu.TotalVRAMBytes == 0 || gpu.FreeVRAMBytes == 0 {
			return selectedPool{}, cannotEstimate("GPU memory capacity is unavailable")
		}
		available := gpu.FreeVRAMBytes
		assumption := "available memory is based on currently free VRAM"
		if req.Engine == model.EngineLlamaCPP && strings.EqualFold(strings.TrimSpace(req.Profile.NGL), "auto") && req.Hardware.RAM.AvailableBytes > 0 {
			return selectedPool{
				total:      gpu.TotalVRAMBytes + req.Hardware.RAM.TotalBytes,
				available:  available + req.Hardware.RAM.AvailableBytes,
				assumption: "llama.cpp NGL=auto is modeled as a combined VRAM and available RAM pool for CPU-offloaded weights",
			}, nil
		}
		if req.Engine == model.EngineVLLM {
			utilization := DefaultVLLMUtilization
			cfg, err := req.Profile.VLLMConfig()
			if err != nil {
				return selectedPool{}, cannotEstimate("vLLM configuration is invalid: %v", err)
			}
			if cfg.GPUUtilization != nil {
				utilization = *cfg.GPUUtilization
			}
			if utilization <= 0 || utilization > 1 {
				return selectedPool{}, cannotEstimate("vLLM GPU memory utilization is outside (0, 1]")
			}
			configured := uint64(float64(gpu.TotalVRAMBytes) * utilization)
			if configured < available {
				available = configured
			}
			assumption = fmt.Sprintf("vLLM may reserve up to %.0f%% of total VRAM for weights, KV cache, CUDA graphs, and runtime buffers", utilization*100)
			return selectedPool{total: gpu.TotalVRAMBytes, available: available, engineBudget: configured, assumption: assumption}, nil
		}
		return selectedPool{total: gpu.TotalVRAMBytes, available: available, assumption: assumption}, nil
	}
	if req.Engine != model.EngineLlamaCPP {
		return selectedPool{}, cannotEstimate("vLLM requires a detected accelerator")
	}
	if req.Hardware.RAM.TotalBytes == 0 || req.Hardware.RAM.AvailableBytes == 0 {
		return selectedPool{}, cannotEstimate("system memory capacity is unavailable")
	}
	return selectedPool{total: req.Hardware.RAM.TotalBytes, available: req.Hardware.RAM.AvailableBytes, assumption: "CPU-only llama.cpp uses available system RAM as the memory pool"}, nil
}

func contextSize(meta model.ModelMetadata, requested int) (uint32, []string) {
	if meta.ContextLength == 0 {
		return 0, nil
	}
	if requested <= 0 {
		return meta.ContextLength, nil
	}
	if uint64(requested) > uint64(meta.ContextLength) {
		return meta.ContextLength, []string{fmt.Sprintf("requested context was capped at the model-native limit of %d tokens", meta.ContextLength)}
	}
	return uint32(requested), nil
}

func weightBytes(meta model.ModelMetadata, req Request) (uint64, Confidence, []string, error) {
	if meta.FileSizeBytes > 0 {
		weights := uint64(meta.FileSizeBytes)
		assumptions := []string{"artifact size is used as the resident weight-memory estimate"}
		if req.Engine == model.EngineLlamaCPP && strings.EqualFold(strings.TrimSpace(req.Profile.NGL), "auto") && meta.NumExperts > 0 && meta.MoEExpertBytes > 0 && meta.MoEExpertBytes < weights && meta.NumExpertsPerToken > 0 && meta.NumExpertsPerToken < meta.NumExperts {
			activeExpertBytes := meta.MoEExpertBytes * uint64(meta.NumExpertsPerToken) / uint64(meta.NumExperts)
			if activeExpertBytes < meta.MoEExpertBytes {
				weights -= meta.MoEExpertBytes - activeExpertBytes
				assumptions = []string{fmt.Sprintf("resident MoE weight estimate keeps %d/%d expert tensors active; %d MiB of inactive experts are CPU-offloaded", meta.NumExpertsPerToken, meta.NumExperts, (meta.MoEExpertBytes-activeExpertBytes)/(1024*1024))}
			}
		}
		return weights, ConfidenceHigh, assumptions, nil
	}
	if meta.ParameterCount == 0 || meta.Quantization == "" {
		return 0, "", nil, cannotEstimate("model weight size and quantization metadata are missing")
	}
	bpp, ok := quantizationBytes(meta.Quantization)
	if !ok {
		return 0, "", nil, cannotEstimate("quantization format is unsupported and artifact size is unavailable")
	}
	return uint64(math.Ceil(float64(meta.ParameterCount) * bpp)), ConfidenceMedium, []string{fmt.Sprintf("weight memory uses %s at %.3f bytes per parameter", meta.Quantization, bpp)}, nil
}

func kvCacheBytes(meta model.ModelMetadata, req Request) (uint64, string, error) {
	bpe := 2.0
	assumption := "KV cache uses fp16 elements"
	if req.Engine == model.EngineLlamaCPP {
		k, err := kvType(req.Profile.CacheTypeK)
		if err != nil {
			return 0, "", err
		}
		v, err := kvType(req.Profile.CacheTypeV)
		if err != nil {
			return 0, "", err
		}
		bpe = max(k, v)
		if req.Profile.CacheTypeK != nil || req.Profile.CacheTypeV != nil {
			assumption = fmt.Sprintf("KV cache uses %.1f bytes per element based on the larger configured K/V type", bpe)
		}
	}
	contextSize := contextFor(req, meta)
	bytes := 2 * uint64(meta.BlockCount) * uint64(meta.AttentionHeadCountKV) * uint64(meta.HeadDimension) * uint64(contextSize)
	if meta.SlidingWindow > 0 {
		fullLayers := min(meta.GlobalAttentionLayers, meta.BlockCount)
		localLayers := meta.BlockCount - fullLayers
		if meta.KVCacheSharedLayers > 0 {
			uniqueLayers := meta.BlockCount
			if meta.KVCacheSharedLayers > uniqueLayers {
				uniqueLayers = 0
			} else {
				uniqueLayers -= meta.KVCacheSharedLayers
			}
			if fullLayers > uniqueLayers {
				fullLayers = uniqueLayers
			}
			localLayers = uniqueLayers - fullLayers
		}
		patternKnown := uint32(len(meta.AttentionLayerTypes)) == meta.BlockCount
		if patternKnown {
			patternLayers := meta.BlockCount
			if meta.KVCacheSharedLayers < patternLayers {
				patternLayers -= meta.KVCacheSharedLayers
			} else {
				patternLayers = 0
			}
			fullLayers = 0
			for _, layerType := range meta.AttentionLayerTypes[:patternLayers] {
				if layerType == "full_attention" {
					fullLayers++
				}
			}
			localLayers = patternLayers
			if fullLayers > localLayers {
				fullLayers = localLayers
			}
			localLayers -= fullLayers
		}
		localContext := min(contextSize, meta.SlidingWindow)
		bytes = 2 * uint64(meta.AttentionHeadCountKV) * uint64(meta.HeadDimension) * (uint64(fullLayers)*uint64(contextSize) + uint64(localLayers)*uint64(localContext))
		assumption = fmt.Sprintf("KV cache models %d full-attention layers and %d sliding-window layers", fullLayers, localLayers)
		if !patternKnown && meta.GlobalAttentionLayers == 0 {
			assumption += "; all layers are assumed to use the configured sliding window because no full-attention pattern was provided"
		}
		if meta.KVCacheSharedLayers > 0 {
			assumption += fmt.Sprintf("; %d shared KV layers are not allocated independently", meta.KVCacheSharedLayers)
		}
	} else {
		assumption += "; KV cache assumes full-context attention because no sliding-window metadata was provided"
	}
	return uint64(math.Ceil(float64(bytes) * bpe)), assumption, nil
}

func contextFor(req Request, meta model.ModelMetadata) uint32 {
	if req.Profile.ContextSize > 0 && uint64(req.Profile.ContextSize) < uint64(meta.ContextLength) {
		return uint32(req.Profile.ContextSize)
	}
	return meta.ContextLength
}

func kvType(raw *string) (float64, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return 2, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case "f16", "fp16", "bf16":
		return 2, nil
	case "f32", "fp32":
		return 4, nil
	case "q8_0", "q8", "i8":
		return 1, nil
	case "q4_0", "q4_1", "q4", "i4", "iq4_nl":
		return 0.5, nil
	case "q5_0", "q5_1":
		return 0.625, nil
	default:
		return 0, cannotEstimate("unsupported KV cache type %q", *raw)
	}
}

func quantizationBytes(raw string) (float64, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "F32":
		return 4, true
	case "F16", "BF16":
		return 2, true
	case "Q8_0", "INT8", "AWQ-8BIT", "GPTQ-INT8":
		return 1, true
	case "Q5_0", "Q5_1", "Q5_K_S", "Q5_K_M":
		return 0.625, true
	case "Q6_K":
		return 0.75, true
	case "Q4_K_S", "Q4_K_M", "Q4_0", "Q4_1", "Q4_0_4_4", "Q4_0_4_8", "Q4_0_8_8", "IQ4_NL", "IQ4_XS", "AWQ-4BIT", "GPTQ-INT4":
		return 0.5, true
	case "Q3_K_S", "Q3_K_M", "Q3_K_L", "IQ3_XS", "IQ3_XXS", "IQ3_S", "IQ3_M":
		return 0.375, true
	case "Q2_K", "Q2_K_S", "IQ2_XXS", "IQ2_XS", "IQ2_S", "IQ2_M":
		return 0.25, true
	case "IQ1_S", "IQ1_M":
		return 0.2, true
	case "TQ1_0":
		return 0.21, true
	case "TQ2_0":
		return 0.32, true
	default:
		return 0, false
	}
}

func classify(utilization, threshold float64) FitLevel {
	if math.IsInf(utilization, 1) || utilization > 1 {
		return FitDoesNotFit
	}
	if utilization > threshold {
		return FitTight
	}
	return FitFits
}

func cannotEstimate(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCannotEstimate, fmt.Sprintf(format, args...))
}
