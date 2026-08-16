package model

import (
	"encoding/json"
	"path/filepath"
)

type ModelType string

const (
	TypeGGUF           ModelType = "gguf"
	TypeGGUFMultimodal ModelType = "gguf_multimodal"
	TypeSafetensors    ModelType = "safetensors"
	TypeHFQuantized    ModelType = "hf_quantized"
)

type ModelEntry struct {
	ID          int64
	ScanDir     string
	ModelDir    string
	PrimaryFile string
	MmprojPath  string
	DisplayName string
	Type        ModelType
	Metadata    string

	SourceRepo     string
	SourceRevision string
	SourceFiles    string
}

func (m ModelEntry) ModelPath() string {
	if m.PrimaryFile != "" {
		return filepath.Join(m.ModelDir, m.PrimaryFile)
	}
	return m.ModelDir
}

type ModelMetadata struct {
	Architecture          string   `json:"architecture"`
	ModelName             string   `json:"model_name,omitempty"`
	ContextLength         uint32   `json:"context_length,omitempty"`
	EmbeddingLength       uint32   `json:"embedding_length,omitempty"`
	BlockCount            uint32   `json:"block_count,omitempty"`
	FeedForwardLength     uint32   `json:"feed_forward_length,omitempty"`
	AttentionHeadCount    uint32   `json:"attention_head_count,omitempty"`
	AttentionHeadCountKV  uint32   `json:"attention_head_count_kv,omitempty"`
	HeadDimension         uint32   `json:"head_dimension,omitempty"`
	FileType              int32    `json:"file_type,omitempty"`
	Quantization          string   `json:"quantization,omitempty"`
	FileSizeBytes         int64    `json:"file_size_bytes,omitempty"`
	TokenizerModel        string   `json:"tokenizer_model,omitempty"`
	ParameterCount        uint64   `json:"parameter_count,omitempty"`
	VocabSize             uint32   `json:"vocab_size,omitempty"`
	NumExperts            uint32   `json:"num_experts,omitempty"`
	NumExpertsPerToken    uint32   `json:"num_experts_per_token,omitempty"`
	AttentionLayerTypes   []string `json:"attention_layer_types,omitempty"`
	SlidingWindow         uint32   `json:"sliding_window,omitempty"`
	GlobalAttentionLayers uint32   `json:"global_attention_layers,omitempty"`
	KVCacheSharedLayers   uint32   `json:"kv_cache_shared_layers,omitempty"`
	MTPNumLayers          uint32   `json:"mtp_num_layers,omitempty"`
	MoEExpertBytes        uint64   `json:"moe_expert_bytes,omitempty"`
}

type EngineKind string

const (
	EngineLlamaCPP EngineKind = "llamacpp"
	EngineVLLM     EngineKind = "vllm"
)

type InferenceEngine struct {
	ID       string
	Name     string
	Kind     EngineKind
	Path     string
	BaseArgs []string
	Env      map[string]string
}

type VLLMConfig struct {
	ServedModelName      string   `json:"served_model_name,omitempty"`
	GPUUtilization       *float64 `json:"gpu_memory_utilization,omitempty"`
	MaxNumSeqs           *int     `json:"max_num_seqs,omitempty"`
	MaxBatchedTokens     *int     `json:"max_num_batched_tokens,omitempty"`
	DType                string   `json:"dtype,omitempty"`
	TensorParallelSize   *int     `json:"tensor_parallel_size,omitempty"`
	PipelineParallelSize *int     `json:"pipeline_parallel_size,omitempty"`
	EnablePrefixCaching  bool     `json:"enable_prefix_caching,omitempty"`
	TrustRemoteCode      bool     `json:"trust_remote_code,omitempty"`
}

type Profile struct {
	ID                int64
	ModelID           int64
	InferenceEngineID string
	Name              string
	Port              int
	Host              string
	ContextSize       int
	NGL               string
	BatchSize         *int
	UBatchSize        *int
	CacheTypeK        *string
	CacheTypeV        *string
	FlashAttn         bool
	Jinja             bool
	Temperature       *float64
	ReasoningBudget   *int
	TopP              *float64
	TopK              *int
	NoKVOffload       bool
	UseMmproj         bool
	ExtraFlags        string
	EngineConfig      string
}

func (p Profile) VLLMConfig() (VLLMConfig, error) {
	var cfg VLLMConfig
	if p.EngineConfig == "" {
		return cfg, nil
	}
	err := json.Unmarshal([]byte(p.EngineConfig), &cfg)
	return cfg, err
}
