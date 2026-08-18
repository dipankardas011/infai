package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/internal/downloader"
	"github.com/dipankardas011/infai/internal/model"
)

func TestDownloadPlanOptionalToggle(t *testing.T) {
	m := DownloadModel{step: stepReviewPlan}
	m.setPlan(&downloader.DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "main",
		EngineKind: model.EngineLlamaCPP,
		Files:      []downloader.PlanFile{{Path: "model.gguf", Size: 100}},
		OptionalFiles: []downloader.PlanFile{
			{Path: "mmproj.gguf", Size: 50},
			{Path: "extra.gguf", Size: 25},
		},
	})

	if len(m.optSel) != 2 || !m.optSel[0] || !m.optSel[1] {
		t.Fatalf("optional files should default to selected: %v", m.optSel)
	}

	allFiles, optStart := m.planFiles()
	if len(allFiles) != 3 || optStart != 1 {
		t.Fatalf("unexpected plan files: len=%d optStart=%d", len(allFiles), optStart)
	}

	m.reviewCursor = 1
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.optSel[0] {
		t.Fatal("expected first optional to be deselected after toggle")
	}
}

func TestDownloadPlanEffectivePruning(t *testing.T) {
	m := DownloadModel{step: stepReviewPlan}
	m.setPlan(&downloader.DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "main",
		EngineKind: model.EngineLlamaCPP,
		Files:      []downloader.PlanFile{{Path: "model.gguf", Size: 100}},
		OptionalFiles: []downloader.PlanFile{
			{Path: "mmproj.gguf", Size: 50},
			{Path: "extra.gguf", Size: 25},
		},
	})

	m.optSel[1] = false
	eff := m.effectivePlan()
	if len(eff.OptionalFiles) != 1 || eff.OptionalFiles[0].Path != "mmproj.gguf" {
		t.Fatalf("expected only mmproj selected, got %+v", eff.OptionalFiles)
	}
	if eff.TotalBytes != 150 {
		t.Fatalf("expected combined total 150, got %d", eff.TotalBytes)
	}
	if len(eff.Files) != 1 {
		t.Fatalf("required files must be unchanged, got %d", len(eff.Files))
	}
}
