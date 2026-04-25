package scanner

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dipankardas011/infai/model"
)

func isMmproj(name string) bool {
	return strings.Contains(strings.ToLower(name), "mmproj")
}

func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// Scan walks one level under dir, returning one ModelEntry per non-mmproj .gguf file.
// The mmproj path (if any) in the same subdir is attached to all sibling entries.
func Scan(dir string) ([]model.ModelEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []model.ModelEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(dir, e.Name())
		files, err := os.ReadDir(subdir)
		if err != nil {
			continue
		}

		var mmproj string
		var mains []string
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".gguf" {
				continue
			}
			if isMmproj(f.Name()) {
				mmproj = filepath.Join(subdir, f.Name())
			} else {
				mains = append(mains, filepath.Join(subdir, f.Name()))
			}
		}

		for _, path := range mains {
			out = append(out, model.ModelEntry{
				DirName:     e.Name(),
				GGUFPath:    path,
				MmprojPath:  mmproj,
				DisplayName: e.Name() + " / " + stem(filepath.Base(path)),
			})
		}
	}
	return out, nil
}
