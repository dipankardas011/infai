package inference

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dipankardas011/infai/internal/model"
)

type MetricsSnapshot struct {
	Timestamp       time.Time
	GenerationTPS   float64
	PrefillTPS      float64
	ActiveRequests  int
	QueuedRequests  int
	GeneratedTokens int64
	PromptTokens    int64
}

type MetricsSource interface {
	Stream(context.Context) <-chan MetricsSnapshot
}

type httpMetricsSource struct {
	kind     model.EngineKind
	url      string
	client   *http.Client
	interval time.Duration
}

type rawMetrics struct {
	generationTPS   float64
	prefillTPS      float64
	active          int
	queued          int
	generatedTokens int64
	promptTokens    int64
	found           bool
}

func newHTTPMetricsSource(kind model.EngineKind, host string, port int) MetricsSource {
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	} else if host == "::" || host == "[::]" {
		host = "::1"
	}
	return &httpMetricsSource{
		kind:     kind,
		url:      fmt.Sprintf("http://%s/metrics", net.JoinHostPort(host, strconv.Itoa(port))),
		client:   &http.Client{Timeout: 2 * time.Second},
		interval: 2 * time.Second,
	}
}

func (s *httpMetricsSource) Stream(ctx context.Context) <-chan MetricsSnapshot {
	out := make(chan MetricsSnapshot, 1)
	go func() {
		defer close(out)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		var previous rawMetrics
		var previousAt time.Time
		collect := func() {
			now := time.Now()
			raw, err := s.collect(ctx)
			if err != nil || !raw.found {
				return
			}
			snapshot := MetricsSnapshot{
				Timestamp: now, GenerationTPS: raw.generationTPS, PrefillTPS: raw.prefillTPS,
				ActiveRequests: raw.active, QueuedRequests: raw.queued,
				GeneratedTokens: raw.generatedTokens, PromptTokens: raw.promptTokens,
			}
			if s.kind == model.EngineVLLM && !previousAt.IsZero() {
				elapsed := now.Sub(previousAt).Seconds()
				if elapsed > 0 {
					if raw.generatedTokens >= previous.generatedTokens {
						snapshot.GenerationTPS = float64(raw.generatedTokens-previous.generatedTokens) / elapsed
					}
					if raw.promptTokens >= previous.promptTokens {
						snapshot.PrefillTPS = float64(raw.promptTokens-previous.promptTokens) / elapsed
					}
				}
			}
			previous, previousAt = raw, now
			select {
			case out <- snapshot:
			default:
			}
		}

		collect()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				collect()
			}
		}
	}()
	return out
}

func (s *httpMetricsSource) collect(ctx context.Context) (rawMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return rawMetrics{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return rawMetrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rawMetrics{}, fmt.Errorf("metrics endpoint returned %s", resp.Status)
	}
	return parseMetrics(s.kind, resp.Body)
}

func parseMetrics(kind model.EngineKind, input io.Reader) (rawMetrics, error) {
	var out rawMetrics
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if idx := strings.IndexByte(name, '{'); idx >= 0 {
			name = name[:idx]
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch kind {
		case "", model.EngineLlamaCPP:
			parseLlamaCPPMetric(&out, name, value)
		case model.EngineVLLM:
			parseVLLMMetric(&out, name, value)
		}
	}
	return out, scanner.Err()
}

func parseLlamaCPPMetric(out *rawMetrics, name string, value float64) {
	switch name {
	case "llamacpp:predicted_tokens_seconds":
		out.generationTPS, out.found = value, true
	case "llamacpp:prompt_tokens_seconds":
		out.prefillTPS, out.found = value, true
	case "llamacpp:requests_processing":
		out.active, out.found = int(value), true
	case "llamacpp:requests_deferred":
		out.queued, out.found = int(value), true
	case "llamacpp:tokens_predicted_total":
		out.generatedTokens, out.found = int64(value), true
	case "llamacpp:prompt_tokens_total":
		out.promptTokens, out.found = int64(value), true
	}
}

func parseVLLMMetric(out *rawMetrics, name string, value float64) {
	switch name {
	case "vllm:num_requests_running":
		out.active += int(value)
		out.found = true
	case "vllm:num_requests_waiting":
		out.queued += int(value)
		out.found = true
	case "vllm:generation_tokens_total":
		out.generatedTokens += int64(value)
		out.found = true
	case "vllm:prompt_tokens_total":
		out.promptTokens += int64(value)
		out.found = true
	}
}
