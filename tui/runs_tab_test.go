package tui

import "testing"

func runColumnTitles(cols []runColumn) []string {
	titles := make([]string, len(cols))
	for i, c := range cols {
		titles[i] = c.title
	}
	return titles
}

func TestVisibleRunColumnsResponsive(t *testing.T) {
	// Wide terminal: every column fits and flex columns absorb the slack.
	wide := visibleRunColumns(110)
	if len(wide) != 7 {
		t.Fatalf("expected all 7 columns at width 110, got %v", runColumnTitles(wide))
	}
	if w := runColumnsWidth(wide); w > 110 {
		t.Fatalf("wide table overflows: %d > 110", w)
	}

	// 60-col terminal (MinWindowWidth): low-priority columns must drop and the
	// remainder must fit.
	narrow := visibleRunColumns(54)
	if w := runColumnsWidth(narrow); w > 54 {
		t.Fatalf("narrow table overflows: %d > 54", w)
	}
	for _, title := range runColumnTitles(narrow) {
		if title == "ENGINE" {
			t.Fatal("ENGINE column should be dropped at narrow widths")
		}
	}
	for _, want := range []string{"RUN", "STATUS", "PROFILE"} {
		found := false
		for _, title := range runColumnTitles(narrow) {
			if title == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("core column %s missing at narrow width, got %v", want, runColumnTitles(narrow))
		}
	}
}

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
