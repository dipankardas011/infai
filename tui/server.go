package tui

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const stopGraceTimeout = 5 * time.Second

var (
	ansiEscape    = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	promptProgress = regexp.MustCompile(`prompt processing progress,.*progress = ([0-9.]+)`)
)

func stripAnsi(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// Tea messages for server I/O.
type logLineMsg string
type serverExitMsg struct{ err error }
type stopTimeoutMsg struct{}

func listenForLog(ch <-chan string, exitCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			err := <-exitCh
			return serverExitMsg{err: err}
		}
		return logLineMsg(line)
	}
}

const maxLogLines = 10000

// ServerModel is screen 5 — shows live llama-server output.
type ServerModel struct {
	cmd             *exec.Cmd
	launchArgs      []string
	logCh           chan string
	exitCh          chan error
	logs            []string
	vp              viewport.Model
	profileName     string
	modelName       string
	modelType       string
	contextSize     int
	host            string
	port            int
	systemUsage     string
	modelUsage      string
	promptProgress  int // 0 = none, 1–100 = active
	startedAt       time.Time
	stoppedAt       time.Time
	stopped         bool
	stopping        bool
	forceKilled     bool
	exitErr         error
	tpsHistory      []float64
	liveTPS         float64
	livePrefillTPS  float64
	liveActive      int
	liveDeferred    int
	liveTotalGen    int64
	liveTotalPrompt int64
	width           int
	height          int
	initialized     bool
}

// NewServerModel starts the server process and returns the model + initial listen cmd.
func NewServerModel(args []string, profileName, modelName, modelType string, contextSize int, host string, port, w, h int) (ServerModel, tea.Cmd, error) {
	cmd := exec.Command(args[0], args[1:]...)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ServerModel{}, nil, err
	}

	logCh := make(chan string, 256)
	exitCh := make(chan error, 1)

	// goroutine: read lines → channel
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			logCh <- stripAnsi(sc.Text())
		}
		close(logCh)
	}()

	// goroutine: wait for exit → close pipe, capture err
	go func() {
		err := cmd.Wait()
		pw.Close()
		exitCh <- err
	}()

	vpH := max(h-7, 5) // initial: 2 header lines; computeVPH() corrects once metrics load
	vp := viewport.New(w-4, vpH)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary)

	m := ServerModel{
		cmd:         cmd,
		launchArgs:  append([]string(nil), args...),
		logCh:       logCh,
		exitCh:      exitCh,
		vp:          vp,
		profileName: profileName,
		modelName:   modelName,
		modelType:   modelType,
		contextSize: contextSize,
		host:        host,
		port:        port,
		startedAt:   time.Now(),
		width:       w,
		height:      h,
		initialized: true,
	}
	return m, tea.Batch(listenForLog(logCh, exitCh), getMetricsCmd(cmd.Process.Pid), getLiveMetricsCmd(host, port)), nil
}

func (s ServerModel) HandleLogLine(line string) (ServerModel, tea.Cmd) {
	s = s.appendLogLine(line)
	s = s.updatePromptProgress(line)
	return s, listenForLog(s.logCh, s.exitCh)
}

func (s ServerModel) updatePromptProgress(line string) ServerModel {
	if strings.Contains(line, "prompt processing done") {
		s.promptProgress = 100
		return s
	}
	if !strings.Contains(line, "prompt processing progress") {
		return s
	}
	matches := promptProgress.FindStringSubmatch(line)
	if len(matches) < 2 {
		return s
	}
	var progress float64
	fmt.Sscanf(matches[1], "%f", &progress)
	s.promptProgress = int(progress * 100)
	return s
}

func (s ServerModel) appendLogLine(line string) ServerModel {
	s.logs = append(s.logs, line)
	if len(s.logs) > maxLogLines {
		s.logs = s.logs[len(s.logs)-maxLogLines:]
	}
	if v, ok := parseGenTPS(line); ok {
		s.tpsHistory = appendTPS(s.tpsHistory, v)
	}
	atBottom := s.vp.AtBottom()
	s.vp.SetContent(strings.Join(s.logs, "\n"))
	if atBottom {
		s.vp.GotoBottom()
	}
	return s
}

func (s ServerModel) SetExited(err error) ServerModel {
	s.stopped = true
	s.stopping = false
	s.exitErr = err
	if s.stoppedAt.IsZero() {
		s.stoppedAt = time.Now()
	}
	return s
}

func (s ServerModel) Restart() (ServerModel, tea.Cmd, error) {
	if !s.stopped || s.stopping {
		return s, nil, fmt.Errorf("server is not stopped")
	}
	if len(s.launchArgs) == 0 {
		return s, nil, fmt.Errorf("missing launch command")
	}
	return NewServerModel(s.launchArgs, s.profileName, s.modelName, s.modelType, s.contextSize, s.host, s.port, s.width, s.height)
}

func (s ServerModel) Stop() (ServerModel, tea.Cmd) {
	if s.cmd == nil || s.cmd.Process == nil || s.stopped || s.stopping {
		return s, nil
	}
	s.stopping = true
	syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	cmd := tea.Tick(stopGraceTimeout, func(time.Time) tea.Msg { return stopTimeoutMsg{} })
	return s, cmd
}

func (s ServerModel) ForceKill() ServerModel {
	if s.cmd != nil && s.cmd.Process != nil && !s.stopped {
		s.forceKilled = true
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	return s
}

// computeVPH derives the viewport content height from the current header line count.
func (s ServerModel) computeVPH() int {
	lines := 2 // line1 (status) + line2 (model)
	if s.systemUsage != "" {
		lines++
	}
	if s.modelUsage != "" {
		lines++
	}
	n := len(s.tpsHistory)
	hasTPS := n > 0 || s.liveTPS > 0 || s.livePrefillTPS > 0
	if hasTPS {
		lines++ // divider
		if s.liveTPS > 0 || n > 0 {
			lines++ // gen line
		}
		if s.livePrefillTPS > 0 || s.liveTotalGen > 0 || s.liveTotalPrompt > 0 {
			lines++ // prefill line
		}
	}
	return max(s.height-lines-5, 5)
}

func (s ServerModel) SetSize(w, h int) ServerModel {
	if !s.initialized {
		return s
	}
	s.width = w
	s.height = h
	s.vp.Width = w - 4
	s.vp.Height = s.computeVPH()
	return s
}

// splitMetricParts splits a metrics string on "  |  " separators,
// renders each part with style, and joins with dot.
func splitMetricParts(metric string, style lipgloss.Style, dot string) string {
	parts := strings.Split(metric, "  |  ")
	rendered := make([]string, len(parts))
	for i, p := range parts {
		rendered[i] = style.Render(strings.TrimSpace(p))
	}
	return strings.Join(rendered, dot)
}

func (s ServerModel) Update(msg tea.Msg) (ServerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case systemMetricsMsg:
		if s.stopped {
			return s, nil
		}
		s.systemUsage = msg.System
		s.modelUsage = msg.Model
		s.vp.Height = s.computeVPH()
		return s, tickMetrics()
	case tickMetricsMsg:
		if s.stopped {
			return s, nil
		}
		if s.cmd == nil || s.cmd.Process == nil {
			return s, nil
		}
		return s, getMetricsCmd(s.cmd.Process.Pid)
	case liveMetricsMsg:
		if s.stopped {
			return s, nil
		}
		if msg.ok {
			s.liveTPS = msg.avgTPS
			s.livePrefillTPS = msg.prefillTPS
			s.liveActive = msg.active
			s.liveDeferred = msg.deferred
			s.liveTotalGen = msg.totalGenTokens
			s.liveTotalPrompt = msg.totalPromptTokens
			s.vp.Height = s.computeVPH()
		}
		return s, tickLiveMetrics()
	case tickLiveMetricsMsg:
		if s.stopped {
			return s, nil
		}
		return s, getLiveMetricsCmd(s.host, s.port)
	case tea.KeyMsg:
		switch msg.String() {
		case "c":
			s.logs = nil
			s.vp.SetContent("")
			return s, nil
		}
	}
	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

func (s ServerModel) View() string {
	t := ActiveTheme
	val := lipgloss.NewStyle().Foreground(t.Secondary)
	hi := lipgloss.NewStyle().Foreground(t.Primary)
	dim := styleMuted

	// ── line 1: identity + status ──────────────────────────────────────────
	status := lipgloss.NewStyle().Foreground(t.Success).Bold(true).Render("● running")
	if s.stopping {
		status = lipgloss.NewStyle().Foreground(t.Error).Bold(true).Render("◌ shutting down…")
	} else if s.stopped {
		label := "■ stopped"
		if s.forceKilled {
			label = "■ force-killed"
		}
		status = dim.Render(label)
	}
	pid := ""
	if s.cmd != nil && s.cmd.Process != nil {
		pid = dim.Render(fmt.Sprintf("  pid:%d", s.cmd.Process.Pid))
	}
	uptime := ""
	if !s.startedAt.IsZero() {
		end := time.Now()
		if s.stopped && !s.stoppedAt.IsZero() {
			end = s.stoppedAt
		}
		uptime = dim.Render("  up:" + end.Sub(s.startedAt).Truncate(time.Second).String())
	}
	executor := ""
	if len(s.launchArgs) > 0 {
		executor = dim.Render("  executor:") + styleKey.Render(filepath.Base(s.launchArgs[0]))
	}
	endpoint := dim.Render("  endpoint:") + styleKey.Render(fmt.Sprintf("http://%s:%d/v1", s.host, s.port))
	line1 := styleTitle.Render(s.profileName) + "  " + status + pid + uptime + executor + endpoint

	// ── line 2: model info ─────────────────────────────────────────────────
	modelMeta := hi.Render(s.modelName)
	if s.contextSize > 0 {
		modelMeta += dim.Render(fmt.Sprintf("   ctx:%d", s.contextSize))
	}
	if s.modelType != "" {
		modelMeta += dim.Render("   type:" + s.modelType)
	}
	line2 := dim.Render("  model   ") + modelMeta

	// ── lines 3-4: hardware resources ─────────────────────────────────────
	dot := dim.Render("  •  ")
	var resourceLines []string
	if s.systemUsage != "" {
		resourceLines = append(resourceLines, dim.Render("  sys     ")+splitMetricParts(s.systemUsage, val, dot))
	}
	if s.modelUsage != "" {
		resourceLines = append(resourceLines, dim.Render("  proc    ")+splitMetricParts(s.modelUsage, val, dot))
	}

	// ── throughput section (divider + gen + prefill) ───────────────────────
	var throughputLines []string
	_, p50, p95, n := computeTPSStats(s.tpsHistory)
	hasTPS := n > 0 || s.liveTPS > 0 || s.livePrefillTPS > 0

	if hasTPS {
		divider := dim.Render("  " + strings.Repeat("─", max(s.width-6, 20)))
		throughputLines = append(throughputLines, divider)

		// gen line: each segment pre-styled, joined with dot
		var genSegs []string
		if s.liveTPS > 0 {
			genSegs = append(genSegs, hi.Render(fmt.Sprintf("%.1f t/s", s.liveTPS)))
		}
		if n >= 5 {
			genSegs = append(genSegs, hi.Render(fmt.Sprintf("p50 %.1f t/s  p95 %.1f t/s", p50, p95)))
		} else if n > 0 {
			genSegs = append(genSegs, hi.Render(fmt.Sprintf("latest %.1f t/s  (warming up)", s.tpsHistory[len(s.tpsHistory)-1])))
		}
		if s.liveActive > 0 {
			genSegs = append(genSegs, lipgloss.NewStyle().Foreground(t.Success).Render(fmt.Sprintf("● %d active", s.liveActive)))
		}
		if s.promptProgress > 0 {
			genSegs = append(genSegs, hi.Render(fmt.Sprintf("%d%% prompt", s.promptProgress)))
		}
		if s.liveDeferred > 0 {
			genSegs = append(genSegs, dim.Render(fmt.Sprintf("%d queued", s.liveDeferred)))
		}
		if len(genSegs) > 0 {
			throughputLines = append(throughputLines,
				dim.Render("  gen     ")+strings.Join(genSegs, dot))
		}

		// prefill line: each segment pre-styled, joined with dot
		var prefillSegs []string
		if s.livePrefillTPS > 0 {
			prefillSegs = append(prefillSegs, val.Render(fmt.Sprintf("%.0f t/s", s.livePrefillTPS)))
		}
		var lifetimeSegs []string
		if s.liveTotalGen > 0 {
			lifetimeSegs = append(lifetimeSegs, val.Render(fmt.Sprintf("%d tokens out", s.liveTotalGen)))
		}
		if s.liveTotalPrompt > 0 {
			lifetimeSegs = append(lifetimeSegs, val.Render(fmt.Sprintf("%d tokens in", s.liveTotalPrompt)))
		}
		if len(lifetimeSegs) > 0 {
			prefillSegs = append(prefillSegs, dim.Render("lifetime: ")+strings.Join(lifetimeSegs, dot))
		}
		if len(prefillSegs) > 0 {
			throughputLines = append(throughputLines,
				dim.Render("  prefill ")+strings.Join(prefillSegs, dot))
		}
	}

	// ── assemble header ────────────────────────────────────────────────────
	parts := []string{line1, line2}
	parts = append(parts, resourceLines...)
	parts = append(parts, throughputLines...)
	header := strings.Join(parts, "\n")

	// Log viewport
	logView := s.vp.View()

	// Footer
	footer := ""
	if s.stopped {
		exitStatus := styleSuccess.Render("exited cleanly")
		if s.exitErr != nil {
			exitStatus = styleError.Render("error: " + s.exitErr.Error())
		}
		footer = "\n" + exitStatus
	}

	return header + "\n\n" + logView + footer
}
