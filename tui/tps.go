package tui

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/model"
)

const maxTPSHistory = 100

// parseGenTPS extracts generation TPS from a llama.cpp stdout timing line.
// Matches "       eval time = ..." but NOT "prompt eval time" or "total time".
func parseGenTPS(line string) (float64, bool) {
	if strings.Contains(line, "prompt eval time") || strings.Contains(line, "total time") {
		return 0, false
	}
	if !strings.Contains(line, "eval time") || !strings.Contains(line, "tokens per second") {
		return 0, false
	}
	idx := strings.Index(line, "tokens per second")
	before := strings.TrimSpace(line[:idx])
	lastComma := strings.LastIndex(before, ",")
	if lastComma < 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(before[lastComma+1:]), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func appendTPS(history []float64, v float64) []float64 {
	history = append(history, v)
	if len(history) > maxTPSHistory {
		history = history[len(history)-maxTPSHistory:]
	}
	return history
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// renderSparkline draws the most recent history samples as a mini bar chart,
// one rune per sample, scaled between the window's min and max.
func renderSparkline(history []float64, width int) string {
	if width <= 0 || len(history) < 2 {
		return ""
	}
	s := history
	if len(s) > width {
		s = s[len(s)-width:]
	}
	lo, hi := s[0], s[0]
	for _, v := range s {
		lo = min(lo, v)
		hi = max(hi, v)
	}
	out := make([]rune, len(s))
	for i, v := range s {
		idx := len(sparkRunes) / 2
		if hi > lo {
			idx = int((v-lo)/(hi-lo)*float64(len(sparkRunes)-1) + 0.5)
		}
		out[i] = sparkRunes[idx]
	}
	return string(out)
}

// computeTPSStats returns latest/p50/p95 and sample count from per-request history.
func computeTPSStats(history []float64) (latest, p50, p95 float64, n int) {
	n = len(history)
	if n == 0 {
		return
	}
	latest = history[n-1]
	sorted := make([]float64, n)
	copy(sorted, history)
	sort.Float64s(sorted)
	p50 = interpPercentile(sorted, 50)
	p95 = interpPercentile(sorted, 95)
	return
}

func interpPercentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 1 {
		return sorted[0]
	}
	idx := p / 100.0 * float64(n-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= n {
		return sorted[n-1]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// liveMetricsMsg carries normalized data polled from an engine's /metrics endpoint.
type liveMetricsMsg struct {
	runID             RunID
	avgTPS            float64
	prefillTPS        float64
	active            int
	deferred          int
	totalGenTokens    int64
	totalPromptTokens int64
	ok                bool
}

type tickLiveMetricsMsg struct{ runID RunID }

func tickLiveMetrics(runID RunID) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return tickLiveMetricsMsg{runID: runID}
	})
}

var metricsHTTPClient = &http.Client{Timeout: 2 * time.Second}

func getLiveMetricsCmd(runID RunID, kind model.EngineKind, host string, port int) tea.Cmd {
	return func() tea.Msg {
		return fetchLiveMetrics(runID, kind, host, port)
	}
}

func fetchLiveMetrics(runID RunID, kind model.EngineKind, host string, port int) liveMetricsMsg {
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	} else if host == "::" || host == "[::]" {
		host = "::1"
	}
	url := fmt.Sprintf("http://%s/metrics", net.JoinHostPort(host, strconv.Itoa(port)))
	resp, err := metricsHTTPClient.Get(url)
	if err != nil {
		return liveMetricsMsg{runID: runID}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return liveMetricsMsg{runID: runID}
	}

	var avgTPS, prefillTPS float64
	var active, deferred int
	var totalGenTokens, totalPromptTokens int64
	found := 0

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		metricName := fields[0]
		if idx := strings.IndexByte(metricName, '{'); idx >= 0 {
			metricName = metricName[:idx]
		}
		switch metricName {
		case "llamacpp:predicted_tokens_seconds":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil && v > 0 {
				avgTPS = v
				found++
			}
		case "llamacpp:prompt_tokens_seconds":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil && v > 0 {
				prefillTPS = v
				found++
			}
		case "llamacpp:requests_processing":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				active = int(v)
				found++
			}
		case "llamacpp:requests_deferred":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				deferred = int(v)
				found++
			}
		case "llamacpp:tokens_predicted_total":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				totalGenTokens = int64(v)
				found++
			}
		case "llamacpp:prompt_tokens_total":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				totalPromptTokens = int64(v)
				found++
			}
		case "vllm:num_requests_running":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				active += int(v)
				found++
			}
		case "vllm:num_requests_waiting":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				deferred += int(v)
				found++
			}
		case "vllm:generation_tokens_total":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				totalGenTokens += int64(v)
				found++
			}
		case "vllm:prompt_tokens_total":
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				totalPromptTokens += int64(v)
				found++
			}
		}
	}
	_ = kind // Kind is part of the collector contract; metric names remain self-describing.
	return liveMetricsMsg{
		runID:  runID,
		avgTPS: avgTPS, prefillTPS: prefillTPS,
		active: active, deferred: deferred,
		totalGenTokens: totalGenTokens, totalPromptTokens: totalPromptTokens,
		ok: found > 0,
	}
}
