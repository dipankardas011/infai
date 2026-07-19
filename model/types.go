package model

import "encoding/json"

type ModelEntry struct {
	ID          int64
	ScanDir     string
	DirName     string
	GGUFPath    string
	MmprojPath  string
	DisplayName string
	Type        string
	Metadata    string
}

type GGUFMetadata struct {
	Architecture string
	ModelName    string
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

// ModelPath returns the local artifact accepted by an inference engine.
// Non-GGUF scanners store the model directory in GGUFPath for schema compatibility.
func (m ModelEntry) ModelPath() string { return m.GGUFPath }
