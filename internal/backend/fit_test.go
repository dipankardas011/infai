package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/internal/db"
	"github.com/dipankardas011/infai/internal/hardware"
	"github.com/dipankardas011/infai/internal/memoryfit"
	"github.com/dipankardas011/infai/internal/model"
)

func TestEvaluateFitUsesCurrentlyFreeVRAM(t *testing.T) {
	base := suggestionRequest(model.EngineLlamaCPP, model.TypeGGUF)
	req := FitRequest{
		Model:    base.Model,
		Engine:   base.Engine,
		Profile:  model.Profile{ID: 7, ModelID: 1, InferenceEngineID: "engine-1", ContextSize: 32768, NGL: "99"},
		Hardware: base.Hardware,
	}

	fit, err := EvaluateFit(req)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Fit != memoryfit.FitFits || fit.Pool != memoryfit.PoolVRAM {
		t.Fatalf("fit with 16 GiB free = %s/%s, want %s/%s", fit.Fit, fit.Pool, memoryfit.FitFits, memoryfit.PoolVRAM)
	}

	// Other processes holding 8 GiB must shrink the pool the verdict is based on.
	req.Hardware.Accelerators[0].FreeVRAMBytes = 8 * 1024 * 1024 * 1024
	fit, err = EvaluateFit(req)
	if err != nil {
		t.Fatal(err)
	}
	if fit.PoolAvailableBytes != req.Hardware.Accelerators[0].FreeVRAMBytes {
		t.Fatalf("pool = %d bytes, want free VRAM %d bytes", fit.PoolAvailableBytes, req.Hardware.Accelerators[0].FreeVRAMBytes)
	}
	if fit.Fit != memoryfit.FitDoesNotFit {
		t.Fatalf("fit with 8 GiB free = %s, want %s", fit.Fit, memoryfit.FitDoesNotFit)
	}

	// NGL=auto scores against VRAM plus available RAM, and says so.
	req.Profile.NGL = "auto"
	fit, err = EvaluateFit(req)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Pool != memoryfit.PoolVRAMRAM {
		t.Fatalf("auto offload pool = %s, want %s", fit.Pool, memoryfit.PoolVRAMRAM)
	}
	if want := req.Hardware.Accelerators[0].FreeVRAMBytes + req.Hardware.RAM.AvailableBytes; fit.PoolAvailableBytes != want {
		t.Fatalf("auto offload pool = %d bytes, want %d", fit.PoolAvailableBytes, want)
	}
}

func TestLaunchDecision(t *testing.T) {
	doesNotFit := memoryfit.Result{
		Fit:                memoryfit.FitDoesNotFit,
		Confidence:         memoryfit.ConfidenceHigh,
		Pool:               memoryfit.PoolVRAM,
		PoolAvailableBytes: 12 * 1024 * 1024 * 1024,
		Breakdown:          memoryfit.Breakdown{RequiredBytes: 21*1024*1024*1024 + 512*1024*1024},
	}
	note, block := LaunchDecision(doesNotFit, nil)
	if !block {
		t.Fatal("a high-confidence does-not-fit must block the launch")
	}
	for _, want := range []string{"does not fit", "21.5 GiB", "12.0 GiB free now in VRAM", "9.5 GiB short", "close other GPU apps"} {
		if !strings.Contains(note, want) {
			t.Fatalf("block note missing %q: %s", want, note)
		}
	}

	// A CPU-only profile must not be told to close GPU apps.
	cpuOnly := doesNotFit
	cpuOnly.Pool = memoryfit.PoolRAM
	if note, _ := LaunchDecision(cpuOnly, nil); strings.Contains(note, "GPU") {
		t.Fatalf("CPU-only advice mentions the GPU: %s", note)
	}

	// A quantization-size fallback is not conclusive enough to refuse a launch.
	estimated := doesNotFit
	estimated.Confidence = memoryfit.ConfidenceMedium
	if note, block = LaunchDecision(estimated, nil); block || !strings.Contains(note, "may not fit") {
		t.Fatalf("medium-confidence failure must warn only: block=%v note=%q", block, note)
	}

	tight := memoryfit.Result{
		Fit:                memoryfit.FitTight,
		Confidence:         memoryfit.ConfidenceHigh,
		Pool:               memoryfit.PoolVRAM,
		PoolAvailableBytes: 12 * 1024 * 1024 * 1024,
		Breakdown:          memoryfit.Breakdown{RequiredBytes: 10 * 1024 * 1024 * 1024},
	}
	if note, block = LaunchDecision(tight, nil); block || !strings.Contains(note, "tight: needs 10.0 GiB") {
		t.Fatalf("tight fit must warn only: block=%v note=%q", block, note)
	}

	// Even when it fits, the launch reports the numbers this snapshot produced.
	fits := memoryfit.Result{
		Fit:                memoryfit.FitFits,
		Confidence:         memoryfit.ConfidenceHigh,
		Pool:               memoryfit.PoolVRAM,
		PoolAvailableBytes: 12 * 1024 * 1024 * 1024,
		Breakdown:          memoryfit.Breakdown{RequiredBytes: 10 * 1024 * 1024 * 1024},
	}
	if note, block = LaunchDecision(fits, nil); block || note != "needs 10.0 GiB, 12.0 GiB free now in VRAM" {
		t.Fatalf("a fitting profile must still report its numbers: block=%v note=%q", block, note)
	}

	if note, block = LaunchDecision(memoryfit.Result{}, memoryfit.ErrCannotEstimate); block || !strings.Contains(note, "fit not verified") {
		t.Fatalf("an inconclusive estimate must warn only: block=%v note=%q", block, note)
	}
}

func TestPlanRunBlocksWhenProfileDoesNotFit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	entry := model.ModelEntry{
		ScanDir: "/models", ModelDir: "/models/big", PrimaryFile: "big.gguf",
		DisplayName: "big", Type: model.TypeGGUF,
		Metadata: `{"architecture":"llama","context_length":65536,"block_count":32,"attention_head_count":32,"attention_head_count_kv":8,"head_dimension":128,"file_size_bytes":4294967296,"quantization":"Q4_K_M"}`,
	}
	if err := database.UpsertModel(&entry); err != nil {
		t.Fatal(err)
	}
	enginePath := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(enginePath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := model.InferenceEngine{ID: "engine", Name: "llama.cpp", Kind: model.EngineLlamaCPP, Path: enginePath}
	if err := database.CreateInferenceEngine(engine); err != nil {
		t.Fatal(err)
	}
	profile := model.Profile{
		ModelID: entry.ID, InferenceEngineID: engine.ID, Name: "big",
		Port: 8080, Host: "127.0.0.1", ContextSize: 65536, NGL: "99",
	}
	if err := database.UpsertProfile(&profile); err != nil {
		t.Fatal(err)
	}

	service := New(database)
	// Another process is holding almost the whole card.
	service.probeHardware = func(context.Context) hardware.Snapshot {
		return hardware.Snapshot{
			RAM: hardware.Memory{TotalBytes: 32 << 30, AvailableBytes: 24 << 30},
			Accelerators: []hardware.Accelerator{{
				Name: "test GPU", Backend: hardware.BackendNVIDIA,
				TotalVRAMBytes: 16 << 30, FreeVRAMBytes: 1 << 30,
			}},
		}
	}

	plan, err := service.PlanRun(entry, profile, 8080)
	if err != nil {
		t.Fatalf("plan run: %v", err)
	}
	if !plan.Blocked {
		t.Fatalf("plan should be blocked, note=%q", plan.Note)
	}
	if !strings.Contains(plan.Note, "does not fit") || !strings.Contains(plan.Note, "1.0 GiB free now in VRAM") {
		t.Fatalf("note does not describe the snapshot: %q", plan.Note)
	}
	if plan.Spec.Launch.Command == "" {
		t.Fatal("plan should still carry the resolved command")
	}
}
