package db

import (
	"reflect"
	"testing"

	"github.com/dipankardas011/infai/model"
)

func seedModelEngineProfile(t *testing.T, d *DB) (model.ModelEntry, model.InferenceEngine, model.Profile) {
	t.Helper()
	m := model.ModelEntry{
		ScanDir:     "/models",
		ModelDir:    "/models/qwen",
		PrimaryFile: "model.gguf",
		MmprojPath:  "/models/qwen/mmproj.gguf",
		DisplayName: "Qwen",
		Type:        model.TypeGGUFMultimodal,
		Metadata:    "{}",
	}
	if err := d.UpsertModel(&m); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	engine := model.InferenceEngine{
		ID: "01900000-0000-7000-8000-000000000001", Name: "test llama.cpp",
		Kind: model.EngineLlamaCPP, Path: "/bin/llama-server",
		Env: map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
	}
	if err := d.CreateInferenceEngine(engine); err != nil {
		t.Fatalf("create inference engine: %v", err)
	}

	batch := 512
	cache := "q4_0"
	temp := 0.7
	topK := 40
	p := model.Profile{
		ModelID:           m.ID,
		InferenceEngineID: engine.ID,
		Name:              "perf",
		Port:              8080,
		Host:              "127.0.0.1",
		ContextSize:       65536,
		NGL:               "auto",
		BatchSize:         &batch,
		CacheTypeK:        &cache,
		FlashAttn:         true,
		Jinja:             true,
		Temperature:       &temp,
		TopK:              &topK,
		UseMmproj:         true,
		ExtraFlags:        "--spec-draft-n 4",
	}
	if err := d.UpsertProfile(&p); err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	if err := d.MarkRecent(m.ID, p.ID); err != nil {
		t.Fatalf("mark recent: %v", err)
	}
	return m, engine, p
}

func TestListAllProfilesLoadsFullProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	m, engine, p := seedModelEngineProfile(t, d)

	entries, err := d.ListAllProfiles()
	if err != nil {
		t.Fatalf("list all profiles: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(entries))
	}
	got := entries[0]
	if got.Model.Type != m.Type || got.Model.Metadata != m.Metadata || got.Model.DisplayName != m.DisplayName {
		t.Fatalf("model not fully loaded: %#v", got.Model)
	}
	if got.Model.ModelDir != m.ModelDir || got.Model.PrimaryFile != m.PrimaryFile {
		t.Fatalf("model dir/file not loaded: %#v", got.Model)
	}
	if !reflect.DeepEqual(got.InferenceEngine, engine) {
		t.Fatalf("inference engine not loaded: %#v", got.InferenceEngine)
	}
	if got.Profile.NGL != "auto" || got.Profile.BatchSize == nil || *got.Profile.BatchSize != *p.BatchSize || got.Profile.CacheTypeK == nil || *got.Profile.CacheTypeK != *p.CacheTypeK || !got.Profile.FlashAttn || !got.Profile.Jinja || got.Profile.Temperature == nil || *got.Profile.Temperature != *p.Temperature || got.Profile.TopK == nil || *got.Profile.TopK != *p.TopK || !got.Profile.UseMmproj || got.Profile.ExtraFlags != p.ExtraFlags {
		t.Fatalf("profile not fully loaded: %#v", got.Profile)
	}
}

func TestDecodeEngineRuntimeDefaultsEmptyJSON(t *testing.T) {
	var engine model.InferenceEngine
	if err := decodeEngineRuntime(&engine, "", "  "); err != nil {
		t.Fatalf("decode empty engine runtime: %v", err)
	}
	if engine.BaseArgs == nil {
		t.Fatal("expected empty base args rather than nil")
	}
	if engine.Env == nil {
		t.Fatal("expected empty environment rather than nil")
	}
}

func TestDeleteInferenceEngineCascadesProfilesAndRecents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, engine, _ := seedModelEngineProfile(t, d)
	if err := d.DeleteInferenceEngine(engine.ID); err != nil {
		t.Fatalf("delete inference engine: %v", err)
	}

	assertCount(t, d, "profiles", 0)
	assertCount(t, d, "recents", 0)
}

func TestRemoveScanDirCascadesModelsProfilesAndRecents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	seedModelEngineProfile(t, d)
	if err := d.AddScanDir("/models"); err != nil {
		t.Fatalf("add scan dir: %v", err)
	}
	if err := d.RemoveScanDir("/models"); err != nil {
		t.Fatalf("remove scan dir: %v", err)
	}

	assertCount(t, d, "scan_dirs", 0)
	assertCount(t, d, "model_registry", 0)
	assertCount(t, d, "profiles", 0)
	assertCount(t, d, "recents", 0)
}

func TestSyncRemovedModelCascadesProfilesAndRecents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	seedModelEngineProfile(t, d)
	removed, _, err := d.Sync(nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed model, got %d", removed)
	}

	assertCount(t, d, "model_registry", 0)
	assertCount(t, d, "profiles", 0)
	assertCount(t, d, "recents", 0)
}

func TestModelRoundTripPreservesAllFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	m := model.ModelEntry{
		ScanDir:     "/home/models",
		ModelDir:    "/home/models/llama-3b",
		PrimaryFile: "llama-3b-q4_k_m.gguf",
		MmprojPath:  "/home/models/llama-3b/mmproj.gguf",
		DisplayName: "Llama 3B Q4_K_M",
		Type:        model.TypeGGUFMultimodal,
		Metadata:    `{"architecture":"llama","parameter_count":3000000000,"context_length":8192}`,
		SourceRepo:  "meta-llama/Llama-3B",
		SourceRevision: "abc123def",
		SourceFiles: `["model.gguf","mmproj.gguf"]`,
	}
	if err := d.UpsertModel(&m); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	models, err := d.ListModels()
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	got := models[0]
	if got.ModelDir != m.ModelDir || got.PrimaryFile != m.PrimaryFile {
		t.Fatalf("model dir/file mismatch: dir=%q file=%q", got.ModelDir, got.PrimaryFile)
	}
	if got.Type != m.Type {
		t.Fatalf("type mismatch: %s vs %s", got.Type, m.Type)
	}
	if got.MmprojPath != m.MmprojPath {
		t.Fatalf("mmproj mismatch: %s vs %s", got.MmprojPath, m.MmprojPath)
	}
	if got.Metadata != m.Metadata {
		t.Fatalf("metadata mismatch: %s vs %s", got.Metadata, m.Metadata)
	}
	if got.SourceRepo != m.SourceRepo {
		t.Fatalf("source_repo mismatch: %s vs %s", got.SourceRepo, m.SourceRepo)
	}
	if got.SourceRevision != m.SourceRevision {
		t.Fatalf("source_revision mismatch: %s vs %s", got.SourceRevision, m.SourceRevision)
	}
	if got.SourceFiles != m.SourceFiles {
		t.Fatalf("source_files mismatch: %s vs %s", got.SourceFiles, m.SourceFiles)
	}
}

func TestModelRoundTripSafetensors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	m := model.ModelEntry{
		ScanDir:     "/home/models",
		ModelDir:    "/home/models/Qwen2.5-Coder-1.5B",
		PrimaryFile: "",
		DisplayName: "Qwen2.5-Coder-1.5B",
		Type:        model.TypeSafetensors,
		Metadata:    `{"architecture":"qwen2","parameter_count":1540000000}`,
	}
	if err := d.UpsertModel(&m); err != nil {
		t.Fatalf("upsert safetensors model: %v", err)
	}

	models, err := d.ListModels()
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	got := models[0]
	if got.ModelDir != m.ModelDir {
		t.Fatalf("model dir mismatch: %q vs %q", got.ModelDir, m.ModelDir)
	}
	if got.PrimaryFile != "" {
		t.Fatalf("expected empty primary file, got %q", got.PrimaryFile)
	}
	if got.ModelPath() != m.ModelDir {
		t.Fatalf("ModelPath mismatch: %q vs %q", got.ModelPath(), m.ModelDir)
	}
}

func TestModelRegistryPreservesProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	m, engine, p := seedModelEngineProfile(t, d)
	assertCount(t, d, "profiles", 1)
	assertCount(t, d, "recents", 1)
	assertCount(t, d, "model_registry", 1)

	entries, err := d.ListAllProfiles()
	if err != nil {
		t.Fatalf("list profiles after v8: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(entries))
	}
	got := entries[0]
	if got.Profile.ID != p.ID || got.Profile.Name != p.Name {
		t.Fatalf("profile mismatch: %#v", got.Profile)
	}
	if got.Model.ID != m.ID || got.Model.DisplayName != m.DisplayName {
		t.Fatalf("model mismatch: %#v", got.Model)
	}
	if got.InferenceEngine.ID != engine.ID {
		t.Fatalf("engine mismatch: %#v", got.InferenceEngine)
	}
}

func assertCount(t *testing.T, d *DB, table string, want int) {
	t.Helper()
	var got int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("expected %s count %d, got %d", table, want, got)
	}
}
