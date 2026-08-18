package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipankardas011/infai/internal/model"
)

func parseSafetensorMetadata(dir string) (*model.ModelMetadata, error) {
	meta := &model.ModelMetadata{}
	configPath := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(configPath)
	if err != nil {
		return meta, nil
	}

	var cfg struct {
		Architectures         []string               `json:"architectures"`
		MaxPositionEmbeddings *int                   `json:"max_position_embeddings"`
		HiddenSize            *int                   `json:"hidden_size"`
		NumHiddenLayers       *int                   `json:"num_hidden_layers"`
		NumAttentionHeads     *int                   `json:"num_attention_heads"`
		NumKeyValueHeads      *int                   `json:"num_key_value_heads"`
		HeadDim               *int                   `json:"head_dim"`
		IntermediateSize      *int                   `json:"intermediate_size"`
		VocabSize             *int                   `json:"vocab_size"`
		NumLocalExperts       *int                   `json:"num_local_experts"`
		NumExpertsPerTok      *int                   `json:"num_experts_per_tok"`
		TorchDType            string                 `json:"torch_dtype"`
		QuantizationConfig    map[string]interface{} `json:"quantization_config"`
		LayerTypes            []string               `json:"layer_types"`
		SlidingWindow         *int                   `json:"sliding_window"`
		NumKVSharedLayers     *int                   `json:"num_kv_shared_layers"`
		NumNextNLayers        *int                   `json:"num_nextn_predict_layers"`
		TextConfig            *struct {
			LayerTypes        []string `json:"layer_types"`
			SlidingWindow     *int     `json:"sliding_window"`
			NumKVSharedLayers *int     `json:"num_kv_shared_layers"`
			NumNextNLayers    *int     `json:"num_nextn_predict_layers"`
		} `json:"text_config"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return meta, nil
	}

	if len(cfg.Architectures) > 0 {
		meta.Architecture = cfg.Architectures[0]
	}
	if cfg.MaxPositionEmbeddings != nil {
		meta.ContextLength = uint32(*cfg.MaxPositionEmbeddings)
	}
	if cfg.HiddenSize != nil {
		meta.EmbeddingLength = uint32(*cfg.HiddenSize)
	}
	if cfg.NumHiddenLayers != nil {
		meta.BlockCount = uint32(*cfg.NumHiddenLayers)
	}
	if cfg.NumAttentionHeads != nil {
		meta.AttentionHeadCount = uint32(*cfg.NumAttentionHeads)
	}
	if cfg.NumKeyValueHeads != nil {
		meta.AttentionHeadCountKV = uint32(*cfg.NumKeyValueHeads)
	}
	if cfg.HeadDim != nil {
		meta.HeadDimension = uint32(*cfg.HeadDim)
	} else if cfg.HiddenSize != nil && cfg.NumAttentionHeads != nil && *cfg.NumAttentionHeads > 0 {
		meta.HeadDimension = uint32(*cfg.HiddenSize) / uint32(*cfg.NumAttentionHeads)
	}
	if cfg.IntermediateSize != nil {
		meta.FeedForwardLength = uint32(*cfg.IntermediateSize)
	}
	if cfg.VocabSize != nil {
		meta.VocabSize = uint32(*cfg.VocabSize)
	}
	if cfg.NumLocalExperts != nil {
		meta.NumExperts = uint32(*cfg.NumLocalExperts)
	}
	if cfg.NumExpertsPerTok != nil {
		meta.NumExpertsPerToken = uint32(*cfg.NumExpertsPerTok)
	}
	meta.AttentionLayerTypes = cfg.LayerTypes
	if cfg.TextConfig != nil {
		if len(meta.AttentionLayerTypes) == 0 {
			meta.AttentionLayerTypes = cfg.TextConfig.LayerTypes
		}
		if cfg.SlidingWindow == nil {
			cfg.SlidingWindow = cfg.TextConfig.SlidingWindow
		}
		if cfg.NumKVSharedLayers == nil {
			cfg.NumKVSharedLayers = cfg.TextConfig.NumKVSharedLayers
		}
		if cfg.NumNextNLayers == nil {
			cfg.NumNextNLayers = cfg.TextConfig.NumNextNLayers
		}
	}
	if cfg.SlidingWindow != nil {
		meta.SlidingWindow = uint32(*cfg.SlidingWindow)
	}
	if cfg.NumKVSharedLayers != nil {
		meta.KVCacheSharedLayers = uint32(*cfg.NumKVSharedLayers)
	}
	if cfg.NumNextNLayers != nil {
		meta.MTPNumLayers = uint32(*cfg.NumNextNLayers)
	}
	for _, layerType := range meta.AttentionLayerTypes {
		if layerType == "full_attention" {
			meta.GlobalAttentionLayers++
		}
	}
	if cfg.TorchDType != "" {
		meta.Quantization = cfg.TorchDType
	}
	if cfg.QuantizationConfig != nil {
		if qm, ok := cfg.QuantizationConfig["quant_method"].(string); ok {
			if qb, ok := cfg.QuantizationConfig["bits"].(float64); ok {
				meta.Quantization = qm + "_" + formatBits(int(qb))
			} else {
				meta.Quantization = qm
			}
		}
	}

	var totalSize int64
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if filepath.Ext(e.Name()) == ".safetensors" {
				info, err := e.Info()
				if err == nil {
					totalSize += info.Size()
				}
			}
		}
	}
	meta.FileSizeBytes = totalSize
	if meta.MTPNumLayers == 0 {
		if index, err := os.ReadFile(filepath.Join(dir, "model.safetensors.index.json")); err == nil && strings.Contains(string(index), "mtp.") {
			meta.MTPNumLayers = 1
		}
	}

	return meta, nil
}

func formatBits(bits int) string {
	switch bits {
	case 4:
		return "4bit"
	case 8:
		return "8bit"
	default:
		return ""
	}
}
