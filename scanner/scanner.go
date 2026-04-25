package scanner

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
)

const (
	GGUF_MAGIC = 0x46554747
)

func isMmproj(name string) bool {
	return strings.Contains(strings.ToLower(name), "mmproj")
}

func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func LoadModelMetadata(dbInstance *db.DB, m *model.ModelEntry) error {
	return dbInstance.UpsertModel(m)
}

func Scan(dirs []string) ([]model.ModelEntry, error) {
	var out []model.ModelEntry
	for _, dir := range dirs {
		models, err := scanDirectory(dir)
		if err != nil {
			continue
		}
		out = append(out, models...)
	}
	return out, nil
}

func scanDirectory(dir string) ([]model.ModelEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var ggufFiles []string
	var mmproj string
	var npzFiles []string
	var safetensorsFiles []string
	var configJson string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		ext := filepath.Ext(name)

		switch {
		case ext == ".gguf":
			if isMmproj(name) {
				mmproj = path
			} else {
				ggufFiles = append(ggufFiles, path)
			}
		case ext == ".npz":
			npzFiles = append(npzFiles, path)
		case ext == ".safetensors":
			safetensorsFiles = append(safetensorsFiles, path)
		case name == "config.json":
			configJson = path
		}
	}

	var models []model.ModelEntry

	// GGUF: Allow multiple .gguf files
	for _, path := range ggufFiles {
		f, err := os.Open(path)
		if err == nil {
			var magic uint32
			if err := binary.Read(f, binary.LittleEndian, &magic); err == nil && magic == GGUF_MAGIC {
				entry := model.ModelEntry{
					ScanDir:     dir,
					DirName:     stem(filepath.Base(path)),
					GGUFPath:    path,
					MmprojPath:  mmproj,
					DisplayName: stem(filepath.Base(path)),
					Type:        "gguf",
				}
				if mmproj != "" {
					entry.Type = "gguf_multimodal"
				}
				models = append(models, entry)
			}
			f.Close()
		}
	}

	// MLX (Old)
	if len(npzFiles) > 0 && configJson != "" {
		entry := model.ModelEntry{
			ScanDir:     dir,
			DirName:     filepath.Base(dir),
			DisplayName: filepath.Base(dir),
			Type:        "mlx",
		}
		models = append(models, entry)
	}

	// Safetensors
	if len(safetensorsFiles) > 0 && configJson != "" {
		modelType := "safetensors"
		metadata := ""

		if b, err := os.ReadFile(configJson); err == nil {
			var cfg map[string]interface{}
			if err := json.Unmarshal(b, &cfg); err == nil {
				if _, ok := cfg["quantization"]; ok {
					modelType = "mlx_quantized"
				} else if _, ok := cfg["quantization_config"]; ok {
					modelType = "hf_quantized"
				}
			}
		}

		entry := model.ModelEntry{
			ScanDir:     dir,
			DirName:     filepath.Base(dir),
			DisplayName: filepath.Base(dir),
			Type:        modelType,
			Metadata:    metadata,
		}
		models = append(models, entry)
	}

	return models, nil
}
