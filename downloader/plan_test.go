package downloader

import (
	"testing"

	"github.com/dipankardas011/infai/hub"
	"github.com/dipankardas011/infai/model"
)

func mkFiles(entries ...hub.FileEntry) []hub.FileEntry {
	return entries
}

func file(path string, size int64) hub.FileEntry {
	return hub.FileEntry{Path: path, Size: size}
}

func fileLFS(path string, size int64, sha string) hub.FileEntry {
	return hub.FileEntry{Path: path, Size: size, LFS: &hub.LFSInfo{SHA256: sha}}
}

func TestPlanSingleGGUF(t *testing.T) {
	files := mkFiles(
		file("model-q4_k_m.gguf", 4_000_000_000),
		file("README.md", 1000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineLlamaCPP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(plan.Files), plan.Files)
	}
	if plan.Files[0].Path != "model-q4_k_m.gguf" {
		t.Fatalf("expected model-q4_k_m.gguf, got %s", plan.Files[0].Path)
	}
	if plan.TotalBytes != 4_000_000_000 {
		t.Fatalf("expected 4GB total, got %d", plan.TotalBytes)
	}
	if len(plan.OptionalFiles) != 0 {
		t.Fatalf("expected no optional files, got %d", len(plan.OptionalFiles))
	}
}

func TestPlanShardedGGUF(t *testing.T) {
	files := mkFiles(
		file("model-00001-of-00003.gguf", 3_000_000_000),
		file("model-00002-of-00003.gguf", 3_000_000_000),
		file("model-00003-of-00003.gguf", 2_000_000_000),
		file("README.md", 1000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineLlamaCPP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("expected 3 shards, got %d", len(plan.Files))
	}
	if plan.TotalBytes != 8_000_000_000 {
		t.Fatalf("expected 8GB total, got %d", plan.TotalBytes)
	}
	for _, f := range plan.Files {
		if f.Path == "README.md" {
			t.Fatal("README.md should not be in plan")
		}
	}
}

func TestPlanGGUFWithMmproj(t *testing.T) {
	files := mkFiles(
		file("llama-7b-q4_k_m.gguf", 4_000_000_000),
		file("mmproj-llama-7b-f16.gguf", 500_000_000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineLlamaCPP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("expected 1 required file, got %d", len(plan.Files))
	}
	if plan.Files[0].Path != "llama-7b-q4_k_m.gguf" {
		t.Fatalf("expected llama-7b-q4_k_m.gguf, got %s", plan.Files[0].Path)
	}
	if len(plan.OptionalFiles) != 1 {
		t.Fatalf("expected 1 optional file, got %d", len(plan.OptionalFiles))
	}
	if plan.OptionalFiles[0].Path != "mmproj-llama-7b-f16.gguf" {
		t.Fatalf("expected mmproj file, got %s", plan.OptionalFiles[0].Path)
	}
	if plan.TotalBytes != 4_500_000_000 {
		t.Fatalf("expected combined total 4500000000, got %d", plan.TotalBytes)
	}
}

func TestPlanGGUFMultipleQuants(t *testing.T) {
	files := mkFiles(
		file("model-q4_k_m.gguf", 4_000_000_000),
		file("model-q8_0.gguf", 7_000_000_000),
		file("model-f16.gguf", 13_000_000_000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineLlamaCPP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("expected 1 file (picks first single), got %d", len(plan.Files))
	}
}

func TestPlanGGUFNoGGUFFiles(t *testing.T) {
	_, err := PlanFiles("org/repo", "abc123", mkFiles(file("README.md", 100)), model.EngineLlamaCPP)
	if err == nil {
		t.Fatal("expected error for no GGUF files")
	}
}

func TestPlanUnshardedSafetensors(t *testing.T) {
	files := mkFiles(
		file("config.json", 500),
		file("tokenizer.json", 2_000_000),
		file("tokenizer_config.json", 300),
		file("model.safetensors", 5_000_000_000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineVLLM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasConfig := false
	hasTokenizer := false
	hasModel := false
	for _, f := range plan.Files {
		switch f.Path {
		case "config.json":
			hasConfig = true
		case "tokenizer.json":
			hasTokenizer = true
		case "model.safetensors":
			hasModel = true
		}
	}
	if !hasConfig || !hasTokenizer || !hasModel {
		t.Fatalf("missing required files: config=%v tokenizer=%v model=%v", hasConfig, hasTokenizer, hasModel)
	}
	if plan.TotalBytes != 5_002_000_800 {
		t.Fatalf("expected 5002000800 bytes, got %d", plan.TotalBytes)
	}
}

func TestPlanIndexedSafetensors(t *testing.T) {
	files := mkFiles(
		file("config.json", 500),
		file("tokenizer.json", 2_000_000),
		file("model.safetensors.index.json", 3000),
		file("model-00001-of-00002.safetensors", 2_500_000_000),
		file("model-00002-of-00002.safetensors", 2_500_000_000),
	)

	idxContent := []byte(`{"weight_map":{"a.weight":"model-00001-of-00002.safetensors","b.weight":"model-00002-of-00002.safetensors"}}`)

	plan, err := PlanSafetensorsWithIndex("org/repo", "abc123", files, idxContent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasIndex := false
	hasShard1 := false
	hasShard2 := false
	for _, f := range plan.Files {
		switch f.Path {
		case "model.safetensors.index.json":
			hasIndex = true
		case "model-00001-of-00002.safetensors":
			hasShard1 = true
		case "model-00002-of-00002.safetensors":
			hasShard2 = true
		}
	}
	if !hasIndex {
		t.Fatal("plan should include index file")
	}
	if !hasShard1 || !hasShard2 {
		t.Fatalf("missing shards: s1=%v s2=%v", hasShard1, hasShard2)
	}
}

func TestPlanIndexedSafetensorsMissingShard(t *testing.T) {
	files := mkFiles(
		file("config.json", 500),
		file("model.safetensors.index.json", 3000),
		file("model-00001-of-00002.safetensors", 2_500_000_000),
	)

	idxContent := []byte(`{"weight_map":{"a.weight":"model-00001-of-00002.safetensors","b.weight":"model-00002-of-00002.safetensors"}}`)

	_, err := PlanSafetensorsWithIndex("org/repo", "abc123", files, idxContent)
	if err == nil {
		t.Fatal("expected error for missing shard")
	}
}

func TestPlanSafetensorsMissingConfig(t *testing.T) {
	files := mkFiles(
		file("model.safetensors", 5_000_000_000),
	)
	_, err := PlanFiles("org/repo", "abc123", files, model.EngineVLLM)
	if err == nil {
		t.Fatal("expected error for missing config.json")
	}
}

func TestValidatePlanPathTraversal(t *testing.T) {
	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "../etc/passwd", Size: 100}},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestValidatePlanDuplicateDest(t *testing.T) {
	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc",
		EngineKind: model.EngineLlamaCPP,
		Files: []PlanFile{
			{Path: "subdir_a/model.gguf", Size: 100},
			{Path: "subdir_b/model.gguf", Size: 200},
		},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("expected duplicate destination error")
	}
}

func TestValidatePlanEmptyFiles(t *testing.T) {
	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc",
		EngineKind: model.EngineLlamaCPP,
		Files:      nil,
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("expected error for empty files")
	}
}

func TestValidatePlanNilPlan(t *testing.T) {
	err := ValidatePlan(nil)
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

func TestPlanGGUFShardedMixedWithSingle(t *testing.T) {
	files := mkFiles(
		file("model-00001-of-00002.gguf", 3_000_000_000),
		file("model-00002-of-00002.gguf", 3_000_000_000),
		file("other-model-q4_k_m.gguf", 4_000_000_000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineLlamaCPP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("expected 2 shard files, got %d", len(plan.Files))
	}
}

func TestPlanSafetensorsAllTokenizerFiles(t *testing.T) {
	files := mkFiles(
		file("config.json", 500),
		file("tokenizer.json", 100),
		file("tokenizer_config.json", 100),
		file("special_tokens_map.json", 100),
		file("vocab.json", 100),
		file("merges.txt", 100),
		file("model.safetensors", 1_000_000),
	)

	plan, err := PlanFiles("org/repo", "abc123", files, model.EngineVLLM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count := 0
	for _, f := range plan.Files {
		if f.Path != "model.safetensors" {
			count++
		}
	}
	if count != 6 {
		t.Fatalf("expected 6 config/tokenizer files, got %d", count)
	}
}

func TestToPlanFiles(t *testing.T) {
	entries := []hub.FileEntry{
		{Path: "a.gguf", Size: 100, LFS: &hub.LFSInfo{SHA256: "abc123"}},
		{Path: "b.gguf", Size: 200},
	}

	result := ToPlanFiles(entries)
	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result))
	}
	if result[0].SHA256 != "abc123" {
		t.Fatalf("expected LFS sha, got %s", result[0].SHA256)
	}
	if result[1].SHA256 != "" {
		t.Fatalf("expected empty sha, got %s", result[1].SHA256)
	}
}

func TestValidatePlanUnsupportedEngine(t *testing.T) {
	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc",
		EngineKind: "unknown",
		Files:      []PlanFile{{Path: "f.gguf", Size: 1}},
	}
	err := ValidatePlan(plan)
	if err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestPlanFilesEmptyList(t *testing.T) {
	_, err := PlanFiles("org/repo", "abc123", nil, model.EngineLlamaCPP)
	if err == nil {
		t.Fatal("expected error for empty files")
	}
}

func TestPlanFilesUnsupportedEngine(t *testing.T) {
	_, err := PlanFiles("org/repo", "abc123", mkFiles(file("f.gguf", 1)), "bad")
	if err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestParseWeightMapDuplicateFiles(t *testing.T) {
	idx := []byte(`{"weight_map":{"a.weight":"model.safetensors","b.weight":"model.safetensors"}}`)
	paths, err := parseWeightMap(idx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 unique path, got %d: %v", len(paths), paths)
	}
}

func TestParseWeightMapEmpty(t *testing.T) {
	_, err := parseWeightMap([]byte(`{"weight_map":{}}`))
	if err == nil {
		t.Fatal("expected error for empty weight map")
	}
}

func TestParseWeightMapInvalidJSON(t *testing.T) {
	_, err := parseWeightMap([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
