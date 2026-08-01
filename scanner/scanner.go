package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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

func LoadModelMetadata(m *model.ModelEntry) error {
	switch m.Type {
	case "gguf", "gguf_multimodal":
		meta, err := ParseGGUF(m.GGUFPath)
		if err != nil {
			return err
		}
		b, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		m.Metadata = string(b)
	case "safetensors", "hf_quantized":
		meta, err := parseSafetensorMetadata(m.GGUFPath)
		if err != nil {
			return err
		}
		b, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		m.Metadata = string(b)
	}
	return nil
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
	var mmprojFiles []string
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
				mmprojFiles = append(mmprojFiles, path)
			} else {
				ggufFiles = append(ggufFiles, path)
			}
		case ext == ".safetensors":
			safetensorsFiles = append(safetensorsFiles, path)
		case name == "config.json":
			configJson = path
		}
	}

	var models []model.ModelEntry

	for _, path := range ggufFiles {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var magic uint32
		magicErr := binaryReadUint32(f, &magic)
		f.Close()
		if magicErr == nil && magic == GGUF_MAGIC {
			ggufStem := stem(filepath.Base(path))
			entry := model.ModelEntry{
				ScanDir:     dir,
				DirName:     ggufStem,
				GGUFPath:    path,
				MmprojPath:  matchMmproj(ggufStem, mmprojFiles),
				DisplayName: ggufStem,
				Type:        "gguf",
			}
			if entry.MmprojPath != "" {
				entry.Type = "gguf_multimodal"
			}
			models = append(models, entry)
		}
	}

	if len(safetensorsFiles) > 0 && configJson != "" {
		modelType := "safetensors"
		if b, err := os.ReadFile(configJson); err == nil {
			var cfg map[string]any
			if json.Unmarshal(b, &cfg) == nil {
				if _, ok := cfg["quantization_config"]; ok {
					modelType = "hf_quantized"
				}
			}
		}
		entry := model.ModelEntry{
			ScanDir:     dir,
			DirName:     filepath.Base(dir),
			GGUFPath:    dir,
			DisplayName: filepath.Base(dir),
			Type:        modelType,
		}
		models = append(models, entry)
	}

	return models, nil
}

func matchMmproj(ggufStem string, mmprojFiles []string) string {
	if len(mmprojFiles) == 0 {
		return ""
	}
	if len(mmprojFiles) == 1 {
		return mmprojFiles[0]
	}

	ggufLower := strings.ToLower(ggufStem)
	bestMatch := ""
	bestScore := 0

	for _, mmproj := range mmprojFiles {
		mmStem := stem(filepath.Base(mmproj))
		clean := strings.ToLower(strings.ReplaceAll(mmStem, "mmproj", ""))
		clean = strings.Trim(clean, "-_.")

		if strings.Contains(clean, ggufLower) || strings.Contains(ggufLower, clean) {
			score := len(clean)
			if score > bestScore {
				bestScore = score
				bestMatch = mmproj
			}
		}
	}

	return bestMatch
}

func binaryReadUint32(f *os.File, v *uint32) error {
	b := make([]byte, 4)
	n, err := f.Read(b)
	if err != nil {
		return err
	}
	if n < 4 {
		return err
	}
	*v = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return nil
}
