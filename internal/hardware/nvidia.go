package hardware

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const nvidiaQuery = "--query-gpu=name,memory.total,memory.free --format=csv,noheader,nounits"

type CommandRunner func(context.Context, string, ...string) ([]byte, error)

type NVIDIAProvider struct {
	run     CommandRunner
	timeout time.Duration
}

func NewNVIDIAProvider() *NVIDIAProvider {
	return &NVIDIAProvider{run: execNVIDIACommand, timeout: 2 * time.Second}
}

func NewNVIDIAProviderWithRunner(run CommandRunner, timeout time.Duration) *NVIDIAProvider {
	if run == nil {
		run = execNVIDIACommand
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &NVIDIAProvider{run: run, timeout: timeout}
}

func (p *NVIDIAProvider) Collect(parent context.Context) ([]Accelerator, []Issue) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()

	out, err := p.run(ctx, "nvidia-smi", strings.Fields(nvidiaQuery)...)
	if err != nil {
		message := err.Error()
		if ctx.Err() != nil {
			message = ctx.Err().Error()
		}
		return nil, []Issue{{Provider: "nvidia", Message: message}}
	}
	accelerators, err := parseNVIDIAOutput(strings.NewReader(string(out)))
	if err != nil {
		return nil, []Issue{{Provider: "nvidia", Message: err.Error()}}
	}
	return accelerators, nil
}

func execNVIDIACommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func parseNVIDIAOutput(r io.Reader) ([]Accelerator, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = 3
	var result []Accelerator
	for row := 1; ; row++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi row %d: %w", row, err)
		}
		name := strings.TrimSpace(record[0])
		if name == "" {
			return nil, fmt.Errorf("parse nvidia-smi row %d: GPU name is empty", row)
		}
		total, err := parseMiB(record[1])
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi row %d total memory: %w", row, err)
		}
		free, err := parseMiB(record[2])
		if err != nil {
			return nil, fmt.Errorf("parse nvidia-smi row %d free memory: %w", row, err)
		}
		if free > total {
			return nil, fmt.Errorf("parse nvidia-smi row %d: free memory exceeds total memory", row)
		}
		result = append(result, Accelerator{
			Name:           name,
			Backend:        BackendNVIDIA,
			TotalVRAMBytes: total,
			FreeVRAMBytes:  free,
		})
	}
	return result, nil
}

func parseMiB(value string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * 1024 * 1024, nil
}
