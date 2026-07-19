package tui

import (
	"testing"
	"time"

	"github.com/dipankardas011/infai/model"
)

func TestVLLMLiveTPSPopulatesHistory(t *testing.T) {
	server := ServerModel{
		runID:           1,
		engineKind:      model.EngineVLLM,
		liveTotalGen:    100,
		liveTotalPrompt: 50,
		liveMetricsAt:   time.Now().Add(-time.Second),
	}

	updated, _ := server.Update(liveMetricsMsg{
		runID:             1,
		ok:                true,
		totalGenTokens:    120,
		totalPromptTokens: 60,
	})
	if len(updated.tpsHistory) != 1 {
		t.Fatalf("expected one TPS history sample, got %d", len(updated.tpsHistory))
	}
	if updated.tpsHistory[0] <= 0 || updated.liveTPS <= 0 {
		t.Fatalf("expected positive derived TPS, got history=%v live=%f", updated.tpsHistory, updated.liveTPS)
	}
}
