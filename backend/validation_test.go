package backend

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dipankardas011/infai/model"
)

func validProfile() *model.Profile {
	batch := 512
	temp := 0.7
	return &model.Profile{
		ModelID:           1,
		InferenceEngineID: "engine-1",
		Name:              "test-profile",
		Port:              8080,
		Host:              "0.0.0.0",
		ContextSize:       4096,
		NGL:               "auto",
		BatchSize:         &batch,
		Temperature:       &temp,
	}
}

func validGGUFModel() *model.ModelEntry {
	return &model.ModelEntry{
		ID:          1,
		DisplayName: "llama-3b",
		Type:        model.TypeGGUF,
		Metadata:    `{"architecture":"llama","context_length":8192}`,
	}
}

func validSafetensorsModel() *model.ModelEntry {
	return &model.ModelEntry{
		ID:          1,
		DisplayName: "qwen-1.5b",
		Type:        model.TypeSafetensors,
		Metadata:    `{"architecture":"qwen2","context_length":32768}`,
	}
}

func validLlamaCPPEngine() *model.InferenceEngine {
	return &model.InferenceEngine{ID: "engine-1", Kind: model.EngineLlamaCPP}
}

func validVLLMEngine() *model.InferenceEngine {
	return &model.InferenceEngine{ID: "engine-1", Kind: model.EngineVLLM}
}

func TestValidateNilProfile(t *testing.T) {
	issues := ValidateProfile(nil, validGGUFModel(), validLlamaCPPEngine())
	assert.True(t, issues.HasErrors())
	assert.Contains(t, issues.Error(), "profile is nil")
}

func TestValidateIdentity(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*model.Profile)
		wantErr string
	}{
		{"empty name", func(p *model.Profile) { p.Name = "" }, "name: profile name is required"},
		{"whitespace name", func(p *model.Profile) { p.Name = "  " }, "name: profile name is required"},
		{"zero model id", func(p *model.Profile) { p.ModelID = 0 }, "model_id: model is required"},
		{"negative model id", func(p *model.Profile) { p.ModelID = -1 }, "model_id: model is required"},
		{"empty engine id", func(p *model.Profile) { p.InferenceEngineID = "" }, "inference_engine_id: inference engine is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			tt.mutate(prof)
			issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
			assert.True(t, issues.HasErrors())
			assert.Contains(t, issues.Error(), tt.wantErr)
		})
	}
}

func TestValidateModelEngineCompatibility(t *testing.T) {
	tests := []struct {
		name        string
		modelType   model.ModelType
		engineKind  model.EngineKind
		expectError bool
		errContains string
	}{
		{"gguf + llamacpp", model.TypeGGUF, model.EngineLlamaCPP, false, ""},
		{"gguf_multimodal + llamacpp", model.TypeGGUFMultimodal, model.EngineLlamaCPP, false, ""},
		{"safetensors + vllm", model.TypeSafetensors, model.EngineVLLM, false, ""},
		{"hf_quantized + vllm", model.TypeHFQuantized, model.EngineVLLM, false, ""},
		{"gguf + vllm", model.TypeGGUF, model.EngineVLLM, true, "requires a llama.cpp engine"},
		{"gguf_multimodal + vllm", model.TypeGGUFMultimodal, model.EngineVLLM, true, "requires a llama.cpp engine"},
		{"safetensors + llamacpp", model.TypeSafetensors, model.EngineLlamaCPP, true, "requires a vLLM engine"},
		{"hf_quantized + llamacpp", model.TypeHFQuantized, model.EngineLlamaCPP, true, "requires a vLLM engine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			m := &model.ModelEntry{ID: 1, DisplayName: "test", Type: tt.modelType}
			engine := &model.InferenceEngine{ID: "e1", Kind: tt.engineKind}
			issues := ValidateProfile(prof, m, engine)
			if tt.expectError {
				assert.True(t, issues.HasErrors(), "expected errors but got none")
				assert.Contains(t, issues.Error(), tt.errContains)
			} else {
				assert.False(t, issues.HasErrors(), "unexpected errors: %v", issues)
			}
		})
	}
}

func TestValidateNetwork(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*model.Profile)
		wantErr string
	}{
		{"port zero", func(p *model.Profile) { p.Port = 0 }, "port must be between 1 and 65535"},
		{"port negative", func(p *model.Profile) { p.Port = -1 }, "port must be between 1 and 65535"},
		{"port too high", func(p *model.Profile) { p.Port = 70000 }, "port must be between 1 and 65535"},
		{"empty host", func(p *model.Profile) { p.Host = "" }, "host is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			tt.mutate(prof)
			issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
			assert.True(t, issues.HasErrors())
			assert.Contains(t, issues.Error(), tt.wantErr)
		})
	}
}

func TestValidateContextSize(t *testing.T) {
	tests := []struct {
		name         string
		contextSize  int
		metadata     string
		expectError  bool
		expectWarn   bool
		warnContains string
	}{
		{"zero context", 0, "", true, false, ""},
		{"negative context", -1, "", true, false, ""},
		{"within model limit", 4096, `{"context_length":8192}`, false, false, ""},
		{"exceeds model limit", 32768, `{"context_length":8192}`, false, true, "exceeds model max context"},
		{"no metadata", 32768, "", false, false, ""},
		{"invalid metadata json", 32768, `{broken`, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			prof.ContextSize = tt.contextSize
			m := validGGUFModel()
			m.Metadata = tt.metadata
			issues := ValidateProfile(prof, m, validLlamaCPPEngine())
			if tt.expectError {
				assert.True(t, issues.HasErrors())
			} else if tt.expectWarn {
				assert.False(t, issues.HasErrors())
				assert.Contains(t, issues.Error(), tt.warnContains)
			} else {
				assert.Empty(t, issues)
			}
		})
	}
}

func TestValidateLlamaCPPNGL(t *testing.T) {
	tests := []struct {
		name   string
		ngl    string
		hasErr bool
	}{
		{"auto", "auto", false},
		{"empty", "", false},
		{"valid integer", "32", false},
		{"invalid", "not-a-number", true},
		{"negative", "-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			prof.NGL = tt.ngl
			issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
			if tt.hasErr {
				assert.True(t, issues.HasErrors())
				assert.Contains(t, issues.Error(), "NGL must be 'auto' or an integer")
			} else {
				assert.False(t, issues.HasErrors())
			}
		})
	}
}

func TestValidateLlamaCPPKVTypes(t *testing.T) {
	f16, q4_0, xyz, empty := "f16", "q4_0", "xyz", ""
	tests := []struct {
		name   string
		value  *string
		hasErr bool
	}{
		{"nil", nil, false},
		{"valid f16", &f16, false},
		{"valid q4_0", &q4_0, false},
		{"invalid", &xyz, true},
		{"invalid empty", &empty, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof := validProfile()
			prof.CacheTypeK = tt.value
			issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
			if tt.hasErr {
				assert.True(t, issues.HasErrors())
			} else {
				assert.False(t, issues.HasErrors())
			}
		})
	}
}

func TestValidateNumericRanges(t *testing.T) {
	zero, negOne, negHalf, oneHalf, ten := 0, -1, -0.5, 1.5, 10.0

	runtest := func(t *testing.T, mutate func(*model.Profile), errContains string) {
		t.Helper()
		prof := validProfile()
		mutate(prof)
		issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
		if errContains != "" {
			assert.NotEmpty(t, issues)
			assert.Contains(t, issues.Error(), errContains)
		} else {
			assert.Empty(t, issues)
		}
	}

	t.Run("batch size zero", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.BatchSize = &zero }, "batch size must be > 0")
	})
	t.Run("batch size negative", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.BatchSize = &negOne }, "batch size must be > 0")
	})
	t.Run("ubatch size zero", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.UBatchSize = &zero }, "micro-batch size must be > 0")
	})
	t.Run("top_p negative", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.TopP = &negHalf }, "top_p must be between 0 and 1")
	})
	t.Run("top_p above 1", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.TopP = &oneHalf }, "top_p must be between 0 and 1")
	})
	t.Run("top_k zero", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.TopK = &zero }, "top_k must be > 0")
	})
	t.Run("reasoning budget zero", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.ReasoningBudget = &zero }, "reasoning budget must be > 0")
	})
	t.Run("temperature out of range", func(t *testing.T) {
		runtest(t, func(pr *model.Profile) { pr.Temperature = &ten }, "temperature should be between 0 and 5")
	})
}

func TestValidateVLLMFields(t *testing.T) {
	gpu85, gpuZero, gpuAbove, tp3, tp2, seqs32, tok4096, zeroInt := 0.85, 0.0, 1.5, 3, 2, 32, 4096, 0

	t.Run("valid vllm config", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{
			GPUUtilization: &gpu85, MaxNumSeqs: &seqs32,
			MaxBatchedTokens: &tok4096, DType: "auto",
			TensorParallelSize: &tp2,
		})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		m := validSafetensorsModel()
		m.Metadata = `{"architecture":"qwen2","attention_head_count":32}`
		issues := ValidateProfile(prof, m, validVLLMEngine())
		assert.False(t, issues.HasErrors())
	})

	t.Run("gpu util zero", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{GPUUtilization: &gpuZero})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		issues := ValidateProfile(prof, validSafetensorsModel(), validVLLMEngine())
		assert.True(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "GPU memory utilization")
	})

	t.Run("gpu util above 1", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{GPUUtilization: &gpuAbove})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		issues := ValidateProfile(prof, validSafetensorsModel(), validVLLMEngine())
		assert.True(t, issues.HasErrors())
	})

	t.Run("invalid dtype", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{DType: "int8"})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		issues := ValidateProfile(prof, validSafetensorsModel(), validVLLMEngine())
		assert.True(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "invalid dtype")
	})

	t.Run("tp does not divide heads", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{TensorParallelSize: &tp3})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		m := validSafetensorsModel()
		m.Metadata = `{"architecture":"llama","attention_head_count":32}`
		issues := ValidateProfile(prof, m, validVLLMEngine())
		assert.True(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "does not divide head count")
	})

	t.Run("hf_quantized dtype mismatch", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{DType: "float16"})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		m := validSafetensorsModel()
		m.Type = model.TypeHFQuantized
		issues := ValidateProfile(prof, m, validVLLMEngine())
		assert.False(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "HF-quantized models use a fixed dtype")
	})

	t.Run("max seqs zero", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{MaxNumSeqs: &zeroInt})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		issues := ValidateProfile(prof, validSafetensorsModel(), validVLLMEngine())
		assert.True(t, issues.HasErrors())
	})
}

func TestValidateExtraFlags(t *testing.T) {
	t.Run("reserved llama.cpp flags", func(t *testing.T) {
		prof := validProfile()
		prof.ExtraFlags = "--metrics --port 9999 -m /other/model"
		issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
		assert.False(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "reserved flags")
	})

	t.Run("reserved vllm flags", func(t *testing.T) {
		cfg, _ := json.Marshal(model.VLLMConfig{DType: "auto"})
		prof := validProfile()
		prof.EngineConfig = string(cfg)
		prof.ExtraFlags = "--host 127.0.0.1 --port 9000"
		issues := ValidateProfile(prof, validSafetensorsModel(), validVLLMEngine())
		assert.False(t, issues.HasErrors())
		assert.Contains(t, issues.Error(), "reserved flags")
	})

	t.Run("no extra flags", func(t *testing.T) {
		prof := validProfile()
		prof.ExtraFlags = ""
		issues := ValidateProfile(prof, validGGUFModel(), validLlamaCPPEngine())
		assert.Empty(t, issues)
	})
}

func TestValidationErrorsMethods(t *testing.T) {
	var ve ValidationErrors
	ve = append(ve, ValidationError{Field: "a", Issue: "error", Severity: SeverityError})
	ve = append(ve, ValidationError{Field: "b", Issue: "warning", Severity: SeverityWarning})

	assert.True(t, ve.HasErrors())
	assert.NotNil(t, ve.Errors())
	assert.Len(t, ve.Warnings(), 1)
	assert.Contains(t, ve.Error(), "a: error")
	assert.Contains(t, ve.Error(), "b: warning")
}
