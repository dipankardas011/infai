package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/model"
)

func TestSaveProfileResolvesDraftModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	target := model.ModelEntry{ScanDir: "/models", ModelDir: "/models/target", PrimaryFile: "target.gguf", DisplayName: "target", Type: model.TypeGGUF, Metadata: `{}`}
	draft := model.ModelEntry{ScanDir: "/models", ModelDir: "/models/draft", PrimaryFile: "draft.gguf", DisplayName: "draft", Type: model.TypeGGUF, Metadata: `{}`}
	for _, entry := range []*model.ModelEntry{&target, &draft} {
		if err := database.UpsertModel(entry); err != nil {
			t.Fatal(err)
		}
	}
	enginePath := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(enginePath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := model.InferenceEngine{ID: "engine", Name: "llama.cpp", Kind: model.EngineLlamaCPP, Path: enginePath}
	if err := database.CreateInferenceEngine(engine); err != nil {
		t.Fatal(err)
	}

	service := New(database)
	draftID := draft.ID
	tokens := 1
	profile := model.Profile{
		ModelID: target.ID, InferenceEngineID: engine.ID, Name: "speculative",
		Port: 8080, Host: "127.0.0.1", ContextSize: 4096, NGL: "auto",
		SpeculativeMode: model.SpeculativeDraftModel, DraftModelID: &draftID, SpeculativeTokens: &tokens,
	}
	if _, err := service.SaveProfile(&profile); err != nil {
		t.Fatalf("save with resolved draft: %v", err)
	}
	if _, err := service.BuildRunSpec(target, profile, 8081); err != nil {
		t.Fatalf("build run spec with resolved draft: %v", err)
	}
}
