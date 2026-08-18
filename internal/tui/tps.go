package tui

import (
	"sort"
)

const maxTPSHistory = 100

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

// computeTPSStats returns latest/p50/p95 and sample count from throughput history.
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
