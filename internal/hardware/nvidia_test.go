package hardware

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseNVIDIAOutput(t *testing.T) {
	accelerators, err := parseNVIDIAOutput(strings.NewReader("NVIDIA RTX 4090, 24564, 12000\nTesla T4, 15360, 8000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(accelerators) != 2 {
		t.Fatalf("got %d accelerators, want 2", len(accelerators))
	}
	if accelerators[0].Backend != BackendNVIDIA || accelerators[0].TotalVRAMBytes != 24564*1024*1024 || accelerators[0].FreeVRAMBytes != 12000*1024*1024 {
		t.Fatalf("unexpected first accelerator: %#v", accelerators[0])
	}
}

func TestParseNVIDIAOutputRejectsInvalidRows(t *testing.T) {
	tests := []string{
		"GPU, total\n",
		"GPU, invalid, 100\n",
		"GPU, 100, 101\n",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := parseNVIDIAOutput(strings.NewReader(input)); err == nil {
				t.Fatal("expected parsing error")
			}
		})
	}
}

func TestNVIDIAProviderUsesCommandOutput(t *testing.T) {
	provider := NewNVIDIAProviderWithRunner(func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command != "nvidia-smi" || len(args) != 2 {
			t.Fatalf("unexpected command: %q %v", command, args)
		}
		return []byte("GPU, 100, 40\n"), nil
	}, time.Second)
	accelerators, issues := provider.Collect(context.Background())
	if len(issues) != 0 || len(accelerators) != 1 {
		t.Fatalf("unexpected result: accelerators=%#v issues=%#v", accelerators, issues)
	}
}

func TestNVIDIAProviderHonorsTimeout(t *testing.T) {
	provider := NewNVIDIAProviderWithRunner(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, 10*time.Millisecond)
	_, issues := provider.Collect(context.Background())
	if len(issues) != 1 || issues[0].Provider != "nvidia" || issues[0].Message != context.DeadlineExceeded.Error() {
		t.Fatalf("unexpected timeout issues: %#v", issues)
	}
}
