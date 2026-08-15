package hardware

import (
	"context"
	"testing"
	"time"
)

type fakeProvider struct {
	accelerators []Accelerator
	issues       []Issue
}

func (p fakeProvider) Collect(context.Context) ([]Accelerator, []Issue) {
	return p.accelerators, p.issues
}

func TestCollectorIncludesProviderDataAndIssues(t *testing.T) {
	provider := fakeProvider{
		accelerators: []Accelerator{{Name: "GPU 0", Backend: BackendNVIDIA, TotalVRAMBytes: 10}},
		issues:       []Issue{{Provider: "rocm", Message: "not available"}},
	}
	snapshot := (&Collector{providers: []Provider{provider}}).Collect(context.Background())
	if snapshot.CollectedAt.IsZero() || time.Since(snapshot.CollectedAt) < 0 {
		t.Fatalf("invalid collection timestamp: %v", snapshot.CollectedAt)
	}
	if len(snapshot.Accelerators) != 1 || snapshot.Accelerators[0].Name != "GPU 0" {
		t.Fatalf("unexpected accelerators: %#v", snapshot.Accelerators)
	}
	if snapshot.AcceleratorCount != 1 {
		t.Fatalf("unexpected accelerator count: %d", snapshot.AcceleratorCount)
	}
	if len(snapshot.Issues) != 1 || snapshot.Issues[0].Provider != "rocm" {
		t.Fatalf("unexpected issues: %#v", snapshot.Issues)
	}
	if snapshot.CPU.Architecture == "" || snapshot.CPU.LogicalCores < 1 {
		t.Fatalf("unexpected CPU inventory: %#v", snapshot.CPU)
	}
}

func TestCollectorCancelledContextReturnsIssue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot := (&Collector{}).Collect(ctx)
	if len(snapshot.Issues) != 1 || snapshot.Issues[0].Provider != "inventory" {
		t.Fatalf("unexpected issues: %#v", snapshot.Issues)
	}
}
