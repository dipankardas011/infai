package db

import (
	"testing"

	"github.com/dipankardas011/infai/model"
)

func seedModelEngineProfile(t *testing.T, d *DB) (model.ModelEntry, model.InferenceEngine, model.Profile) {
	t.Helper()
	m := model.ModelEntry{
		ScanDir:     "/models",
		DirName:     "qwen",
		GGUFPath:    "/models/qwen/model.gguf",
		MmprojPath:  "/models/qwen/mmproj.gguf",
		DisplayName: "Qwen",
		Type:        "gguf_multimodal",
		Metadata:    "{}",
	}
	if err := d.UpsertModel(&m); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	engine := model.InferenceEngine{ID: "01900000-0000-7000-8000-000000000001", Name: "test llama.cpp", Path: "/bin/llama-server"}
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
	if got.Model.Type != m.Type || got.Model.Metadata != m.Metadata || got.Model.DirName != m.DirName {
		t.Fatalf("model not fully loaded: %#v", got.Model)
	}
	if got.InferenceEngine != engine {
		t.Fatalf("inference engine not loaded: %#v", got.InferenceEngine)
	}
	if got.Profile.NGL != "auto" || got.Profile.BatchSize == nil || *got.Profile.BatchSize != *p.BatchSize || got.Profile.CacheTypeK == nil || *got.Profile.CacheTypeK != *p.CacheTypeK || !got.Profile.FlashAttn || !got.Profile.Jinja || got.Profile.Temperature == nil || *got.Profile.Temperature != *p.Temperature || got.Profile.TopK == nil || *got.Profile.TopK != *p.TopK || !got.Profile.UseMmproj || got.Profile.ExtraFlags != p.ExtraFlags {
		t.Fatalf("profile not fully loaded: %#v", got.Profile)
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
	assertCount(t, d, "models", 0)
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

	assertCount(t, d, "models", 0)
	assertCount(t, d, "profiles", 0)
	assertCount(t, d, "recents", 0)
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
