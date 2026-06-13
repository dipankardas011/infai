package db

import (
	"testing"

	"github.com/dipankardas011/infai/model"
)

func TestListAllProfilesLoadsFullProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	d, err := Open()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

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

	batch := 512
	cache := "q4_0"
	temp := 0.7
	topK := 40
	p := model.Profile{
		ModelID:     m.ID,
		Name:        "perf",
		Port:        8080,
		Host:        "127.0.0.1",
		ContextSize: 65536,
		NGL:         "auto",
		BatchSize:   &batch,
		CacheTypeK:  &cache,
		FlashAttn:   true,
		Jinja:       true,
		Temperature: &temp,
		TopK:        &topK,
		UseMmproj:   true,
		ExtraFlags:  "--spec-draft-n 4",
	}
	if err := d.UpsertProfile(&p); err != nil {
		t.Fatalf("upsert profile: %v", err)
	}

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
	if got.Profile.NGL != "auto" || got.Profile.BatchSize == nil || *got.Profile.BatchSize != batch || got.Profile.CacheTypeK == nil || *got.Profile.CacheTypeK != cache || !got.Profile.FlashAttn || !got.Profile.Jinja || got.Profile.Temperature == nil || *got.Profile.Temperature != temp || got.Profile.TopK == nil || *got.Profile.TopK != topK || !got.Profile.UseMmproj || got.Profile.ExtraFlags != p.ExtraFlags {
		t.Fatalf("profile not fully loaded: %#v", got.Profile)
	}
}
