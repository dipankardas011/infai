package tui

import "testing"

func TestParseResourceMetricsBuildSystemUsageFormat(t *testing.T) {
	metrics := parseResourceMetrics("cpu 45%  |  ram 8.2/32.0GiB 25%  |  nvidia-smi gpu0 60% 12.0/24.0GiB")
	if len(metrics) != 4 {
		t.Fatalf("expected 4 metrics, got %d: %#v", len(metrics), metrics)
	}

	assertMetric := func(i int, label, detail string, percent float64) {
		t.Helper()
		if metrics[i].label != label {
			t.Fatalf("metric %d label: got %q want %q", i, metrics[i].label, label)
		}
		if metrics[i].detail != detail {
			t.Fatalf("metric %d detail: got %q want %q", i, metrics[i].detail, detail)
		}
		if metrics[i].percent != percent {
			t.Fatalf("metric %d percent: got %.2f want %.2f", i, metrics[i].percent, percent)
		}
	}

	assertMetric(0, "cpu", "45%", 45)
	assertMetric(1, "ram", "8.2/32.0GiB 25%", 25)
	assertMetric(2, "gpu0", "60%", 60)
	assertMetric(3, "gpu0 vram", "12.0/24.0GiB", 50)
}

func TestParseResourceMetricsMultipleGPUFormat(t *testing.T) {
	metrics := parseResourceMetrics("cpu 10%  |  ram 4.0/16.0GiB 25%  |  nvidia-smi gpu0 60% 12.0/24.0GiB  |  gpu1 90% 20.0/24.0GiB")
	if len(metrics) != 6 {
		t.Fatalf("expected 6 metrics, got %d: %#v", len(metrics), metrics)
	}
	if metrics[4].label != "gpu1" || metrics[5].label != "gpu1 vram" {
		t.Fatalf("expected gpu1 metrics, got %#v", metrics[4:])
	}
}
