package tui

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type systemMetricsMsg struct {
	System string
	Model  string
}

type tickMetricsMsg time.Time

func fetchSystemMetrics(pid int) (string, string) {
	return buildSystemUsage(), buildModelUsage(pid)
}

func mibToGiB(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v / 1024.0, true
}

func buildSystemUsage() string {
	parts := make([]string, 0, 4)

	if cpuPercents, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(cpuPercents) > 0 {
		parts = append(parts, fmt.Sprintf("cpu %.0f%%", cpuPercents[0]))
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		usedGiB := float64(vm.Used) / 1024.0 / 1024.0 / 1024.0
		totalGiB := float64(vm.Total) / 1024.0 / 1024.0 / 1024.0
		parts = append(parts, fmt.Sprintf("ram %.1f/%.1fGiB %.0f%%", usedGiB, totalGiB, vm.UsedPercent))
	}

	if gpus := readSystemGPUUsage(); gpus != "" {
		parts = append(parts, "nvidia-smi "+gpus)
	}

	if len(parts) == 0 {
		return "n/a"
	}
	return strings.Join(parts, "  |  ")
}

func buildModelUsage(pid int) string {
	parts := make([]string, 0, 3)

	if cpuPercent, rssGiB, ok := readProcessCPUAndRAM(pid); ok {
		parts = append(parts, fmt.Sprintf("cpu %.1f%%", cpuPercent))
		parts = append(parts, fmt.Sprintf("ram %.2fGiB", rssGiB))
	}

	if vramGiB, ok := readProcessGPUVRAM(pid); ok {
		parts = append(parts, fmt.Sprintf("nvidia-smi vram %.2fGiB", vramGiB))
	}

	if len(parts) == 0 {
		return "n/a"
	}
	return strings.Join(parts, "  |  ")
}
func readSystemGPUUsage() string {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return ""
	}
	cmd := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	parts := make([]string, 0, len(lines))
	for i, line := range lines {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		util := strings.TrimSpace(f[0])
		usedGiB, okUsed := mibToGiB(f[1])
		totalGiB, okTotal := mibToGiB(f[2])
		if !okUsed || !okTotal {
			continue
		}
		parts = append(parts, fmt.Sprintf("gpu%d %s%% %.1f/%.1fGiB", i, util, usedGiB, totalGiB))
	}
	return strings.Join(parts, "  |  ")
}

func readProcessCPUAndRAM(pid int) (float64, float64, bool) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, 0, false
	}
	cpuPercent, err := p.Percent(200 * time.Millisecond)
	if err != nil {
		return 0, 0, false
	}
	memInfo, err := p.MemoryInfo()
	if err != nil {
		return 0, 0, false
	}
	rssGiB := float64(memInfo.RSS) / 1024.0 / 1024.0 / 1024.0
	return cpuPercent, rssGiB, true
}

func readProcessGPUVRAM(pid int) (float64, bool) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return 0, false
	}
	cmd := exec.Command("nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, false
	}
	pidStr := strconv.Itoa(pid)
	var totalMiB float64
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 2 {
			continue
		}
		if strings.TrimSpace(f[0]) != pidStr {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		if err != nil {
			continue
		}
		totalMiB += v
	}
	if totalMiB <= 0 {
		return 0, false
	}
	return totalMiB / 1024.0, true
}

func tickMetrics() tea.Cmd {
	return tea.Tick(time.Second*2, func(t time.Time) tea.Msg {
		return tickMetricsMsg(t)
	})
}

func getMetricsCmd(pid int) tea.Cmd {
	return func() tea.Msg {
		systemLine, modelLine := fetchSystemMetrics(pid)
		return systemMetricsMsg{System: systemLine, Model: modelLine}
	}
}
