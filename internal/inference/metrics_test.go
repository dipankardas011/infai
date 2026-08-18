package inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dipankardas011/infai/internal/model"
)

func TestParseLlamaCPPMetrics(t *testing.T) {
	raw, err := parseMetrics(model.EngineLlamaCPP, strings.NewReader(`
llamacpp:predicted_tokens_seconds 42.5
llamacpp:prompt_tokens_seconds 120.25
llamacpp:requests_processing 2
llamacpp:requests_deferred 1
llamacpp:tokens_predicted_total 900
llamacpp:prompt_tokens_total 300
`))
	if err != nil {
		t.Fatal(err)
	}
	if !raw.found || raw.generationTPS != 42.5 || raw.prefillTPS != 120.25 || raw.active != 2 || raw.queued != 1 || raw.generatedTokens != 900 || raw.promptTokens != 300 {
		t.Fatalf("unexpected llama.cpp metrics: %#v", raw)
	}
}

func TestParseVLLMMetricsAggregatesLabels(t *testing.T) {
	raw, err := parseMetrics(model.EngineVLLM, strings.NewReader(`
vllm:num_requests_running{engine="0",model_name="qwen"} 2
vllm:num_requests_waiting{engine="0",model_name="qwen"} 1
vllm:generation_tokens_total{engine="0",model_name="qwen"} 1103
vllm:prompt_tokens_total{engine="0",model_name="qwen"} 7447
`))
	if err != nil {
		t.Fatal(err)
	}
	if !raw.found || raw.active != 2 || raw.queued != 1 || raw.generatedTokens != 1103 || raw.promptTokens != 7447 {
		t.Fatalf("unexpected vLLM metrics: %#v", raw)
	}
}

func TestVLLMMetricsStreamCalculatesRatesAndStops(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = fmt.Fprintf(w, "vllm:generation_tokens_total %d\nvllm:prompt_tokens_total %d\n", requests*20, requests*10)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	source := &httpMetricsSource{
		kind: model.EngineVLLM, url: server.URL,
		client: server.Client(), interval: 10 * time.Millisecond,
	}
	stream := source.Stream(ctx)
	first := receiveSnapshot(t, stream)
	second := receiveSnapshot(t, stream)
	if first.GenerationTPS != 0 || second.GenerationTPS <= 0 || second.PrefillTPS <= 0 {
		t.Fatalf("unexpected rates: first=%#v second=%#v", first, second)
	}

	cancel()
	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("metrics stream did not stop after cancellation")
		}
	}
}

func receiveSnapshot(t *testing.T, stream <-chan MetricsSnapshot) MetricsSnapshot {
	t.Helper()
	select {
	case snapshot, ok := <-stream:
		if !ok {
			t.Fatal("metrics stream closed unexpectedly")
		}
		return snapshot
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for metrics snapshot")
		return MetricsSnapshot{}
	}
}
