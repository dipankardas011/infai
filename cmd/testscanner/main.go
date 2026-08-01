package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dipankardas011/infai/scanner"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: testscanner <path-to-model>\n")
		os.Exit(1)
	}
	path := os.Args[1]

	result, err := inspect(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(result)
}

func inspect(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}

	if !info.IsDir() {
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".gguf" {
			meta, err := scanner.ParseGGUF(path)
			if err != nil {
				return "", fmt.Errorf("parse gguf: %w", err)
			}
			b, _ := json.MarshalIndent(meta, "", "  ")
			return fmt.Sprintf("=== GGUF: %s ===\n%s", filepath.Base(path), string(b)), nil
		}
		return "", fmt.Errorf("unsupported file: %s", path)
	}

	entries, err := scanner.Scan([]string{path})
	if err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no models found in %s", path)
	}

	var out strings.Builder
	out.WriteString(fmt.Sprintf("=== DIR: %s (%d entries) ===\n", filepath.Base(path), len(entries)))

	for i := range entries {
		out.WriteString(fmt.Sprintf("\n--- entry %d: %s (type=%s) ---\n", i+1, entries[i].DisplayName, entries[i].Type))
		out.WriteString(fmt.Sprintf("  ModelDir:    %s\n", entries[i].ModelDir))
		out.WriteString(fmt.Sprintf("  PrimaryFile: %s\n", entries[i].PrimaryFile))
		out.WriteString(fmt.Sprintf("  MmprojPath:  %s\n", entries[i].MmprojPath))
		out.WriteString(fmt.Sprintf("  ModelPath:   %s\n", entries[i].ModelPath()))

		if err := scanner.LoadModelMetadata(&entries[i]); err != nil {
			out.WriteString(fmt.Sprintf("  metadata error: %v\n", err))
			continue
		}

		if entries[i].Metadata != "" {
			var pretty map[string]interface{}
			if json.Unmarshal([]byte(entries[i].Metadata), &pretty) == nil {
				b, _ := json.MarshalIndent(pretty, "  ", "  ")
				out.WriteString(fmt.Sprintf("  Metadata:\n  %s\n", string(b)))
			} else {
				out.WriteString(fmt.Sprintf("  Metadata (raw): %s\n", entries[i].Metadata))
			}
		} else {
			out.WriteString("  Metadata: (empty)\n")
		}
	}

	return out.String(), nil
}
