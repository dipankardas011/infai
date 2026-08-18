package hardware

import (
	"context"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

type Backend string

const (
	BackendCPU    Backend = "cpu"
	BackendNVIDIA Backend = "nvidia"
	BackendROCm   Backend = "rocm"
	BackendMetal  Backend = "metal"
	BackendIntel  Backend = "intel"
)

type Memory struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

type CPU struct {
	Architecture string
	LogicalCores int
}

type Accelerator struct {
	Name           string
	Backend        Backend
	TotalVRAMBytes uint64
	FreeVRAMBytes  uint64
	UnifiedMemory  bool
}

type Issue struct {
	Provider string
	Message  string
}

type Snapshot struct {
	CollectedAt      time.Time
	RAM              Memory
	CPU              CPU
	AcceleratorCount int
	Accelerators     []Accelerator
	Issues           []Issue
}

type Provider interface {
	Collect(context.Context) ([]Accelerator, []Issue)
}

type Collector struct {
	providers []Provider
}

func NewCollector(providers ...Provider) *Collector {
	if len(providers) == 0 {
		providers = []Provider{NewNVIDIAProvider()}
	}
	return &Collector{providers: providers}
}

func Collect(ctx context.Context) Snapshot {
	return NewCollector().Collect(ctx)
}

func (c *Collector) Collect(ctx context.Context) Snapshot {
	snapshot := Snapshot{CollectedAt: time.Now().UTC(), CPU: CPU{Architecture: runtime.GOARCH}}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		snapshot.Issues = append(snapshot.Issues, Issue{Provider: "inventory", Message: err.Error()})
		return snapshot
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		snapshot.Issues = append(snapshot.Issues, Issue{Provider: "system-memory", Message: err.Error()})
	} else {
		snapshot.RAM = Memory{TotalBytes: vm.Total, AvailableBytes: vm.Available}
	}

	if cores, err := cpu.CountsWithContext(ctx, false); err != nil {
		snapshot.Issues = append(snapshot.Issues, Issue{Provider: "cpu", Message: err.Error()})
	} else {
		snapshot.CPU.LogicalCores = cores
	}

	for _, provider := range c.providers {
		accelerators, issues := provider.Collect(ctx)
		snapshot.Accelerators = append(snapshot.Accelerators, accelerators...)
		snapshot.Issues = append(snapshot.Issues, issues...)
	}
	snapshot.AcceleratorCount = len(snapshot.Accelerators)
	return snapshot
}
