package backend

import (
	"fmt"

	"github.com/dipankardas011/infai/internal/hardware"
	"github.com/dipankardas011/infai/internal/memoryfit"
	"github.com/dipankardas011/infai/internal/model"
)

type FitRequest struct {
	Model    model.ModelEntry
	Engine   model.InferenceEngine
	Profile  model.Profile
	Hardware hardware.Snapshot
	Policy   memoryfit.Policy
}

// EvaluateFit scores an existing profile without deriving or changing any of
// its settings. The fit recorded when a profile was created goes stale as other
// processes take and release memory, so callers re-run this against a fresh
// hardware snapshot before launching.
func EvaluateFit(req FitRequest) (memoryfit.Result, error) {
	engineKind, err := resolveEngineKind(req.Engine.Kind)
	if err != nil {
		return memoryfit.Result{}, err
	}
	return memoryfit.Estimate(memoryfit.Request{
		Model:    req.Model,
		Engine:   engineKind,
		Profile:  req.Profile,
		Hardware: req.Hardware,
		Policy:   req.Policy,
	})
}

// LaunchDecision turns a fit estimate into the sentence shown to the user and,
// when the estimate is conclusive, a refusal to launch.
func LaunchDecision(fit memoryfit.Result, err error) (note string, block bool) {
	if err != nil {
		return fmt.Sprintf("fit not verified: %v", err), false
	}
	required := memoryfit.FormatBytes(fit.Breakdown.RequiredBytes)
	available := fmt.Sprintf("%s free now in %s", memoryfit.FormatBytes(fit.PoolAvailableBytes), poolLabel(fit.Pool))
	switch {
	case fit.BlocksLaunch():
		shortfall := uint64(0)
		if fit.Breakdown.RequiredBytes > fit.PoolAvailableBytes {
			shortfall = fit.Breakdown.RequiredBytes - fit.PoolAvailableBytes
		}
		return fmt.Sprintf("does not fit: needs %s, only %s (%s short) - %s",
			required, available, memoryfit.FormatBytes(shortfall), poolAdvice(fit.Pool)), true
	case fit.Fit == memoryfit.FitDoesNotFit:
		return fmt.Sprintf("may not fit: needs %s, %s (weight size is a quantized estimate)", required, available), false
	case fit.Fit == memoryfit.FitTight:
		return fmt.Sprintf("tight: needs %s, %s", required, available), false
	}
	return fmt.Sprintf("needs %s, %s", required, available), false
}

func resolveEngineKind(kind model.EngineKind) (model.EngineKind, error) {
	switch kind {
	case "":
		return model.EngineLlamaCPP, nil
	case model.EngineLlamaCPP, model.EngineVLLM:
		return kind, nil
	default:
		return "", fmt.Errorf("unsupported inference engine kind %q", kind)
	}
}

func poolLabel(kind memoryfit.PoolKind) string {
	switch kind {
	case memoryfit.PoolVRAM:
		return "VRAM"
	case memoryfit.PoolVRAMRAM:
		return "VRAM + RAM"
	case memoryfit.PoolRAM:
		return "system RAM"
	case memoryfit.PoolUnified:
		return "unified memory"
	default:
		return "the memory pool"
	}
}

func poolAdvice(kind memoryfit.PoolKind) string {
	if kind == memoryfit.PoolRAM {
		return "close other apps or lower the context size"
	}
	return "close other GPU apps or lower the context size"
}
