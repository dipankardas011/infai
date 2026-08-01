package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/dipankardas011/infai/model"
)

func parseSafetensorMetadata(dir string) (*model.ModelMetadata, error) {
	meta := &model.ModelMetadata{}
	configPath := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(configPath)
	if err != nil {
		return meta, nil
	}

	var cfg struct {
		Architectures        []string               `json:"architectures"`
		MaxPositionEmbeddings *int                   `json:"max_position_embeddings"`
		HiddenSize           *int                    `json:"hidden_size"`
		NumHiddenLayers      *int                    `json:"num_hidden_layers"`
		NumAttentionHeads    *int                    `json:"num_attention_heads"`
		NumKeyValueHeads     *int                    `json:"num_key_value_heads"`
		HeadDim              *int                    `json:"head_dim"`
		IntermediateSize     *int                    `json:"intermediate_size"`
		VocabSize            *int                    `json:"vocab_size"`
		TorchDType           string                  `json:"torch_dtype"`
		QuantizationConfig   map[string]interface{}  `json:"quantization_config"`
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
