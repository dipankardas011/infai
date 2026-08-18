package tui

import (
	"testing"

	"github.com/dipankardas011/infai/internal/inference"
)

func TestEngineMetricsPopulateHistory(t *testing.T) {
	server := ServerModel{
		runID: 1,
	}

	updated, _ := server.Update(engineMetricsMsg{
		runID: 1,
		snapshot: inference.MetricsSnapshot{
			GenerationTPS: 20, PrefillTPS: 10,
			GeneratedTokens: 120, PromptTokens: 60,
		},
	})
	if len(updated.tpsHistory) != 1 {
		t.Fatalf("expected one TPS history sample, got %d", len(updated.tpsHistory))
	}
	if updated.tpsHistory[0] <= 0 || updated.liveTPS <= 0 {
		t.Fatalf("expected positive derived TPS, got history=%v live=%f", updated.tpsHistory, updated.liveTPS)
	}
}

func TestIdleMetricsRetainLatestThroughput(t *testing.T) {
	server := ServerModel{
		runID:          1,
		liveTPS:        71,
		livePrefillTPS: 121,
		tpsHistory:     []float64{71},
	}

	updated, _ := server.Update(engineMetricsMsg{
		runID: 1,
		snapshot: inference.MetricsSnapshot{
			ActiveRequests: 0, QueuedRequests: 0,
			GeneratedTokens: 1364, PromptTokens: 2270,
		},
	})
	if updated.liveTPS != 71 || updated.livePrefillTPS != 121 {
		t.Fatalf("latest throughput was cleared: gen=%f prefill=%f", updated.liveTPS, updated.livePrefillTPS)
	}
	if len(updated.tpsHistory) != 1 {
		t.Fatalf("idle snapshot should not extend history: %v", updated.tpsHistory)
	}
}
