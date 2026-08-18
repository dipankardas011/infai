package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dipankardas011/infai/internal/model"
)

type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "unknown"
	}
}

type ValidationError struct {
	Field    string
	Issue    string
	Severity Severity
}

func (ve ValidationError) Error() string {
	return ve.Field + ": " + ve.Issue
}

type ValidationErrors []ValidationError

// ValidationIssues extracts structured validation issues from wrapped errors.
func ValidationIssues(err error) []ValidationError {
	var out []ValidationError
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		if issue, ok := current.(ValidationError); ok {
			out = append(out, issue)
			return
		}
		if many, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range many.Unwrap() {
				visit(child)
			}
			return
		}
		if one, ok := current.(interface{ Unwrap() error }); ok {
			visit(one.Unwrap())
		}
	}
	visit(err)
	return out
}

func (ve ValidationErrors) Error() string {
	if len(ve) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range ve {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

func (ve ValidationErrors) HasErrors() bool {
	for _, e := range ve {
		if e.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (ve ValidationErrors) Errors() error {
	var errs []error
	for _, e := range ve {
		if e.Severity == SeverityError {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}

func (ve ValidationErrors) Warnings() []ValidationError {
	var out []ValidationError
	for _, e := range ve {
		if e.Severity == SeverityWarning {
			out = append(out, e)
		}
	}
	return out
}

var validKVTypes = map[string]bool{
	"f32": true, "f16": true, "bf16": true,
	"q8_0": true, "q4_0": true, "q4_1": true,
	"iq4_nl": true, "q5_0": true, "q5_1": true,
}

var validVLLMDTypes = map[string]bool{
	"auto": true, "float16": true, "bfloat16": true, "float32": true,
}

var reservedLlamaCPPFlags = []string{"--metrics", "--port", "--host", "-m", "--mmproj", "-c", "-ngl",
	"-b", "-ub", "--cache-type-k", "--cache-type-v", "--flash-attn", "--jinja",
	"--temperature", "--reasoning-budget", "--top_p", "--top_k", "--no-kv-offload",
	"--spec-type", "--spec-draft-model", "--spec-draft-n-max"}

var reservedVLLMFlags = []string{"--host", "--port", "--max-model-len",
	"--gpu-memory-utilization", "--max-num-seqs", "--max-num-batched-tokens",
	"--dtype", "--tensor-parallel-size", "--pipeline-parallel-size",
	"--enable-prefix-caching", "--trust-remote-code", "--served-model-name", "--speculative-config"}

func ValidateProfile(p *model.Profile, m *model.ModelEntry, engine *model.InferenceEngine, draftModels ...*model.ModelEntry) ValidationErrors {
	var issues ValidationErrors

	if p == nil {
		issues = append(issues, ValidationError{Field: "profile", Issue: "profile is nil", Severity: SeverityError})
		return issues
	}

	validateIdentity(p, &issues)
	validateModelEngineCompatibility(p, m, engine, &issues)
	validateNetwork(p, &issues)
	validateContextSize(p, m, &issues)
	var draft *model.ModelEntry
	if len(draftModels) > 0 {
		draft = draftModels[0]
	}
	validateSpeculative(p, m, draft, engine, &issues)
	validateLlamaCPPFields(p, engine, &issues)
	validateVLLMFields(p, engine, m, &issues)
	validateExtraFlags(p, engine, &issues)

	return issues
}

func validateSpeculative(p *model.Profile, target, draft *model.ModelEntry, engine *model.InferenceEngine, issues *ValidationErrors) {
	if p.SpeculativeMode != model.SpeculativeOff && p.SpeculativeTokens == nil {
		*issues = append(*issues, ValidationError{Field: "speculative_tokens", Issue: "speculative tokens are required when speculative decoding is enabled", Severity: SeverityError})
	} else if p.SpeculativeTokens != nil && *p.SpeculativeTokens <= 0 {
		*issues = append(*issues, ValidationError{Field: "speculative_tokens", Issue: "speculative tokens must be > 0", Severity: SeverityError})
	}
	var kind model.EngineKind
	if engine != nil {
		kind = engine.Kind
		if kind == "" {
			kind = model.EngineLlamaCPP
		}
	}

	switch p.SpeculativeMode {
	case model.SpeculativeOff:
		return
	case model.SpeculativeNativeMTP:
		validateNativeMTPMetadata(target, issues)
	case model.SpeculativeDraftModel, model.SpeculativeMTPAssistant:
		if p.DraftModelID == nil || *p.DraftModelID <= 0 {
			*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "a draft model is required for this speculative mode", Severity: SeverityError})
			return
		}
		if *p.DraftModelID == p.ModelID {
			*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "draft model must be different from the target model", Severity: SeverityError})
			return
		}
		if draft == nil {
			*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "selected draft model does not exist in the model registry", Severity: SeverityError})
			return
		}
	default:
		*issues = append(*issues, ValidationError{Field: "speculative_mode", Issue: fmt.Sprintf("unsupported speculative mode %q", p.SpeculativeMode), Severity: SeverityError})
		return
	}

	if engine == nil {
		return
	}
	if draft == nil {
		return
	}
	switch kind {
	case model.EngineLlamaCPP:
		if draft.Type != model.TypeGGUF && draft.Type != model.TypeGGUFMultimodal {
			*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "llama.cpp requires a GGUF draft model", Severity: SeverityError})
		}
	case model.EngineVLLM:
		if draft.Type != model.TypeSafetensors && draft.Type != model.TypeHFQuantized {
			*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "vLLM requires a safetensors or HF-quantized draft model", Severity: SeverityError})
		}
	}
	validateDraftCompatibility(p.SpeculativeMode, target, draft, issues)
}

func validateDraftCompatibility(mode model.SpeculativeMode, target, draft *model.ModelEntry, issues *ValidationErrors) {
	var targetMeta, draftMeta model.ModelMetadata
	targetOK := target != nil && json.Unmarshal([]byte(target.Metadata), &targetMeta) == nil
	draftOK := draft != nil && json.Unmarshal([]byte(draft.Metadata), &draftMeta) == nil
	established := false

	if targetOK && draftOK {
		if targetMeta.VocabSize > 0 && draftMeta.VocabSize > 0 {
			established = true
			if targetMeta.VocabSize != draftMeta.VocabSize {
				*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: fmt.Sprintf("draft vocabulary size %d does not match target vocabulary size %d", draftMeta.VocabSize, targetMeta.VocabSize), Severity: SeverityError})
			}
		}
		if targetMeta.TokenizerModel != "" && draftMeta.TokenizerModel != "" {
			established = true
			if targetMeta.TokenizerModel != draftMeta.TokenizerModel {
				*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: fmt.Sprintf("draft tokenizer %q does not match target tokenizer %q", draftMeta.TokenizerModel, targetMeta.TokenizerModel), Severity: SeverityError})
			}
		}
	}
	if !established {
		*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "draft tokenizer compatibility cannot be verified from model metadata", Severity: SeverityWarning})
	}

	if mode == model.SpeculativeMTPAssistant && (!draftOK || (draftMeta.MTPNumLayers == 0 && !strings.Contains(strings.ToLower(draftMeta.Architecture), "assistant") && !strings.Contains(strings.ToLower(draftMeta.Architecture), "mtp"))) {
		*issues = append(*issues, ValidationError{Field: "draft_model_id", Issue: "selected model metadata does not identify an MTP assistant; verify that it is built for this target", Severity: SeverityWarning})
	}
}

func validateNativeMTPMetadata(target *model.ModelEntry, issues *ValidationErrors) {
	if target == nil {
		*issues = append(*issues, ValidationError{Field: "speculative_mode", Issue: "native MTP support cannot be verified because the target model is not in the model registry", Severity: SeverityError})
		return
	}
	if strings.TrimSpace(target.Metadata) == "" {
		*issues = append(*issues, ValidationError{Field: "speculative_mode", Issue: "native MTP requires model metadata, but metadata is absent", Severity: SeverityError})
		return
	}
	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(target.Metadata), &meta); err != nil {
		*issues = append(*issues, ValidationError{Field: "speculative_mode", Issue: fmt.Sprintf("native MTP support cannot be verified because model metadata is malformed: %v", err), Severity: SeverityError})
		return
	}
	if meta.MTPNumLayers == 0 {
		*issues = append(*issues, ValidationError{Field: "speculative_mode", Issue: "model metadata does not report native MTP layers", Severity: SeverityError})
	}
}

func validateIdentity(p *model.Profile, issues *ValidationErrors) {
	if strings.TrimSpace(p.Name) == "" {
		*issues = append(*issues, ValidationError{Field: "name", Issue: "profile name is required", Severity: SeverityError})
	}
	if p.ModelID <= 0 {
		*issues = append(*issues, ValidationError{Field: "model_id", Issue: "model is required", Severity: SeverityError})
	}
	if strings.TrimSpace(p.InferenceEngineID) == "" {
		*issues = append(*issues, ValidationError{Field: "inference_engine_id", Issue: "inference engine is required", Severity: SeverityError})
	}
}

func validateModelEngineCompatibility(p *model.Profile, m *model.ModelEntry, engine *model.InferenceEngine, issues *ValidationErrors) {
	_ = p
	if m == nil || engine == nil {
		return
	}

	kind := engine.Kind
	if kind == "" {
		kind = model.EngineLlamaCPP
	}

	switch m.Type {
	case model.TypeGGUF, model.TypeGGUFMultimodal:
		if kind != model.EngineLlamaCPP {
			*issues = append(*issues, ValidationError{
				Field:    "inference_engine",
				Issue:    fmt.Sprintf("GGUF model %q requires a llama.cpp engine, got %q", m.DisplayName, kind),
				Severity: SeverityError,
			})
		}
	case model.TypeSafetensors, model.TypeHFQuantized:
		if kind != model.EngineVLLM {
			*issues = append(*issues, ValidationError{
				Field:    "inference_engine",
				Issue:    fmt.Sprintf("safetensors model %q requires a vLLM engine, got %q", m.DisplayName, kind),
				Severity: SeverityError,
			})
		}
	}
}

func validateNetwork(p *model.Profile, issues *ValidationErrors) {
	if p.Port < 1 || p.Port > 65535 {
		*issues = append(*issues, ValidationError{Field: "port", Issue: "port must be between 1 and 65535", Severity: SeverityError})
	}
	if strings.TrimSpace(p.Host) == "" {
		*issues = append(*issues, ValidationError{Field: "host", Issue: "host is required", Severity: SeverityError})
	}
}

func validateContextSize(p *model.Profile, m *model.ModelEntry, issues *ValidationErrors) {
	if p.ContextSize <= 0 {
		*issues = append(*issues, ValidationError{Field: "context_size", Issue: "context size must be > 0", Severity: SeverityError})
		return
	}

	if m == nil || m.Metadata == "" {
		return
	}

	var meta model.ModelMetadata
	if err := json.Unmarshal([]byte(m.Metadata), &meta); err != nil {
		return
	}

	if meta.ContextLength > 0 && uint32(p.ContextSize) > meta.ContextLength {
		*issues = append(*issues, ValidationError{
			Field:    "context_size",
			Issue:    fmt.Sprintf("profile context (%d) exceeds model max context (%d); the model may not support this", p.ContextSize, meta.ContextLength),
			Severity: SeverityWarning,
		})
	}
}

func validateLlamaCPPFields(p *model.Profile, engine *model.InferenceEngine, issues *ValidationErrors) {
	if engine == nil {
		return
	}
	kind := engine.Kind
	if kind == "" {
		kind = model.EngineLlamaCPP
	}
	if kind != model.EngineLlamaCPP {
		return
	}

	if p.NGL != "" && p.NGL != "auto" {
		if _, err := strconv.Atoi(p.NGL); err != nil {
			*issues = append(*issues, ValidationError{Field: "ngl", Issue: "NGL must be 'auto' or an integer", Severity: SeverityError})
		}
	}

	if p.BatchSize != nil && *p.BatchSize <= 0 {
		*issues = append(*issues, ValidationError{Field: "batch_size", Issue: "batch size must be > 0", Severity: SeverityError})
	}
	if p.UBatchSize != nil && *p.UBatchSize <= 0 {
		*issues = append(*issues, ValidationError{Field: "ubatch_size", Issue: "micro-batch size must be > 0", Severity: SeverityError})
	}

	validateLlamaCPPKVType(p.CacheTypeK, "cache_type_k", issues)
	validateLlamaCPPKVType(p.CacheTypeV, "cache_type_v", issues)

	if p.UseMmproj && p.ModelID > 0 {
		// Validation that model actually has mmproj happens at a higher level.
	}

	if p.Temperature != nil {
		if *p.Temperature < 0 || *p.Temperature > 5 {
			*issues = append(*issues, ValidationError{Field: "temperature", Issue: "temperature should be between 0 and 5", Severity: SeverityWarning})
		}
	}
	if p.TopP != nil {
		if *p.TopP < 0 || *p.TopP > 1 {
			*issues = append(*issues, ValidationError{Field: "top_p", Issue: "top_p must be between 0 and 1", Severity: SeverityError})
		}
	}
	if p.TopK != nil {
		if *p.TopK <= 0 {
			*issues = append(*issues, ValidationError{Field: "top_k", Issue: "top_k must be > 0", Severity: SeverityError})
		}
	}
	if p.ReasoningBudget != nil {
		if *p.ReasoningBudget <= 0 {
			*issues = append(*issues, ValidationError{Field: "reasoning_budget", Issue: "reasoning budget must be > 0", Severity: SeverityError})
		}
	}
}

func validateLlamaCPPKVType(val *string, field string, issues *ValidationErrors) {
	if val == nil {
		return
	}
	if !validKVTypes[*val] {
		*issues = append(*issues, ValidationError{
			Field:    field,
			Issue:    fmt.Sprintf("invalid cache type %q; must be one of: f32, f16, bf16, q8_0, q4_0, q4_1, iq4_nl, q5_0, q5_1", *val),
			Severity: SeverityError,
		})
	}
}

func validateVLLMFields(p *model.Profile, engine *model.InferenceEngine, m *model.ModelEntry, issues *ValidationErrors) {
	if engine == nil {
		return
	}
	kind := engine.Kind
	if kind == "" {
		kind = model.EngineLlamaCPP
	}
	if kind != model.EngineVLLM {
		return
	}

	if p.EngineConfig == "" {
		return
	}

	cfg, err := p.VLLMConfig()
	if err != nil {
		*issues = append(*issues, ValidationError{Field: "engine_config", Issue: fmt.Sprintf("invalid vLLM config JSON: %v", err), Severity: SeverityError})
		return
	}

	if cfg.GPUUtilization != nil {
		if *cfg.GPUUtilization <= 0 || *cfg.GPUUtilization > 1 {
			*issues = append(*issues, ValidationError{Field: "gpu_memory_utilization", Issue: "GPU memory utilization must be > 0 and <= 1", Severity: SeverityError})
		}
	}
	if cfg.MaxNumSeqs != nil && *cfg.MaxNumSeqs <= 0 {
		*issues = append(*issues, ValidationError{Field: "max_num_seqs", Issue: "max sequences must be > 0", Severity: SeverityError})
	}
	if cfg.MaxBatchedTokens != nil && *cfg.MaxBatchedTokens <= 0 {
		*issues = append(*issues, ValidationError{Field: "max_batched_tokens", Issue: "max batched tokens must be > 0", Severity: SeverityError})
	}
	if cfg.PipelineParallelSize != nil && *cfg.PipelineParallelSize <= 0 {
		*issues = append(*issues, ValidationError{Field: "pipeline_parallel_size", Issue: "pipeline parallel size must be > 0", Severity: SeverityError})
	}

	if cfg.DType != "" && !validVLLMDTypes[cfg.DType] {
		*issues = append(*issues, ValidationError{Field: "dtype", Issue: fmt.Sprintf("invalid dtype %q; must be one of: auto, float16, bfloat16, float32", cfg.DType), Severity: SeverityError})
	}

	if cfg.TensorParallelSize != nil && *cfg.TensorParallelSize > 1 && m != nil && m.Metadata != "" {
		var meta model.ModelMetadata
		if err := json.Unmarshal([]byte(m.Metadata), &meta); err == nil {
			if meta.AttentionHeadCount > 0 && meta.AttentionHeadCount%uint32(*cfg.TensorParallelSize) != 0 {
				*issues = append(*issues, ValidationError{
					Field:    "tensor_parallel_size",
					Issue:    fmt.Sprintf("tensor parallel size %d does not divide head count %d", *cfg.TensorParallelSize, meta.AttentionHeadCount),
					Severity: SeverityError,
				})
			}
		}
	}

	if m != nil && m.Type == model.TypeHFQuantized && cfg.DType != "" && cfg.DType != "auto" {
		*issues = append(*issues, ValidationError{
			Field:    "dtype",
			Issue:    "HF-quantized models use a fixed dtype; setting --dtype may cause a mismatch",
			Severity: SeverityWarning,
		})
	}
}

func validateExtraFlags(p *model.Profile, engine *model.InferenceEngine, issues *ValidationErrors) {
	if p.ExtraFlags == "" || engine == nil {
		return
	}
	flagTokens := strings.Fields(p.ExtraFlags)

	kind := engine.Kind
	if kind == "" {
		kind = model.EngineLlamaCPP
	}

	var reserved []string
	switch kind {
	case model.EngineLlamaCPP:
		reserved = reservedLlamaCPPFlags
	case model.EngineVLLM:
		reserved = reservedVLLMFlags
	default:
		return
	}

	reservedSet := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		reservedSet[r] = true
	}

	var conflicts []string
	for _, tok := range flagTokens {
		if reservedSet[tok] {
			conflicts = append(conflicts, tok)
		}
	}
	if len(conflicts) > 0 {
		*issues = append(*issues, ValidationError{
			Field:    "extra_flags",
			Issue:    fmt.Sprintf("extra flags contain reserved flags: %s; these are managed by the profile and may cause conflicts", strings.Join(conflicts, ", ")),
			Severity: SeverityWarning,
		})
	}
}
