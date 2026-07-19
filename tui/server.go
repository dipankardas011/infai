package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/inference"
	"github.com/dipankardas011/infai/runner"
)

const stopGraceTimeout = 5 * time.Second

// Tea messages for server I/O.
type logLineMsg struct {
	runID RunID
	line  string
}
type serverExitMsg struct {
	runID RunID
	err   error
}
type stopTimeoutMsg struct{ runID RunID }
type engineMetricsMsg struct {
	runID    RunID
	snapshot inference.MetricsSnapshot
}

func listenForLog(runID RunID, ch <-chan string, exitCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			err := <-exitCh
			return serverExitMsg{runID: runID, err: err}
		}
		return logLineMsg{runID: runID, line: line}
	}
}

func listenForEngineMetrics(runID RunID, ch <-chan inference.MetricsSnapshot) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		snapshot, ok := <-ch
		if !ok {
			return nil
		}
		return engineMetricsMsg{runID: runID, snapshot: snapshot}
	}
}

const maxLogLines = 10000

// ServerModel is screen 5 and presents one running inference server.
type ServerModel struct {
	runID           RunID
	process         *runner.ServerProcess
	runSpec         inference.RunSpec
	metricsCh       <-chan inference.MetricsSnapshot
	cancelMetrics   context.CancelFunc
	logCh           <-chan string
	exitCh          <-chan error
	logs            []string
	styledLogs      []string // logs with error/warn highlighting, kept in lockstep
	vp              viewport.Model
	profileName     string
	modelName       string
	modelType       string
	contextSize     int
	host            string
	port            int
	systemUsage     string
	modelUsage      string
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
func NewServerModel(runID RunID, spec inference.RunSpec, profileName, modelName, modelType string, contextSize int, host string, port, w, h int) (ServerModel, tea.Cmd, error) {
	process, err := runner.StartServer(spec.Launch)
	if err != nil {
		return ServerModel{}, nil, err
	}
	logCh := process.Logs()
	exitCh := process.Exits()
	metricsCtx, cancelMetrics := context.WithCancel(context.Background())
	var metricsCh <-chan inference.MetricsSnapshot
	if spec.Metrics != nil {
		metricsCh = spec.Metrics.Stream(metricsCtx)
	}

	vpH := max(h-7, 1) // initial: 2 header lines; computeVPH() corrects once metrics load
	vp := viewport.New(w-4, vpH)
	// Use bubbles/viewport native horizontal scrolling instead of drawing a
	// fake scrollbar. Left/right now actually scroll long log lines.
	vp.SetHorizontalStep(8)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary)

	m := ServerModel{
		runID:         runID,
		process:       process,
		runSpec:       inference.RunSpec{Launch: process.Spec(), Metrics: spec.Metrics},
		metricsCh:     metricsCh,
		cancelMetrics: cancelMetrics,
		logCh:         logCh,
		exitCh:        exitCh,
		vp:            vp,
		profileName:   profileName,
		modelName:     modelName,
		modelType:     modelType,
		contextSize:   contextSize,
		host:          host,
		port:          port,
		startedAt:     process.StartedAt(),
		width:         w,
		height:        h,
		initialized:   true,
	}
	return m, tea.Batch(listenForLog(runID, logCh, exitCh), getMetricsCmd(runID, process.PID()), listenForEngineMetrics(runID, metricsCh)), nil
}

func (s ServerModel) HandleLogLine(line string) (ServerModel, tea.Cmd) {
	s = s.appendLogLine(line)
	return s, listenForLog(s.runID, s.logCh, s.exitCh)
}

// styleLogLine highlights error and warning lines so problems stand out while
// logs stream. The raw line is kept separately for clipboard copy.
func styleLogLine(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "error") || strings.Contains(l, "failed") || strings.Contains(l, "fatal"):
		return styleError.Render(line)
	case strings.Contains(l, "warn"):
		return styleWarning.Render(line)
	}
	return line
}

func (s ServerModel) appendLogLine(line string) ServerModel {
	s.logs = append(s.logs, line)
	s.styledLogs = append(s.styledLogs, styleLogLine(line))
	if len(s.logs) > maxLogLines {
		s.logs = s.logs[len(s.logs)-maxLogLines:]
		s.styledLogs = s.styledLogs[len(s.styledLogs)-maxLogLines:]
	}
	atBottom := s.vp.AtBottom()
	s.vp.SetContent(strings.Join(s.styledLogs, "\n"))
	if atBottom {
		s.vp.GotoBottom()
	}
	return s
}

func (s ServerModel) SetExited(err error) ServerModel {
	if s.cancelMetrics != nil {
		s.cancelMetrics()
	}
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
	if s.runSpec.Launch.Command == "" {
		return s, nil, fmt.Errorf("missing launch command")
	}
	return NewServerModel(s.runID, s.runSpec, s.profileName, s.modelName, s.modelType, s.contextSize, s.host, s.port, s.width, s.height)
}

func (s ServerModel) Stop() (ServerModel, tea.Cmd) {
	if s.process == nil || s.process.PID() == 0 || s.stopped || s.stopping {
		return s, nil
	}
	s.stopping = true
	_ = s.process.Stop()
	cmd := tea.Tick(stopGraceTimeout, func(time.Time) tea.Msg { return stopTimeoutMsg{runID: s.runID} })
	return s, cmd
}

func (s ServerModel) ForceKill() ServerModel {
	if s.process != nil && s.process.PID() != 0 && !s.stopped {
		s.forceKilled = true
		_ = s.process.ForceKill()
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
	return max(s.height-lines-5, 1)
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
		if msg.runID != s.runID || s.stopped {
			return s, nil
		}
		s.systemUsage = msg.System
		s.modelUsage = msg.Model
		s.vp.Height = s.computeVPH()
		return s, tickMetrics(s.runID)
	case tickMetricsMsg:
		if msg.runID != s.runID || s.stopped {
			return s, nil
		}
		if s.process == nil || s.process.PID() == 0 {
			return s, nil
		}
		return s, getMetricsCmd(s.runID, s.process.PID())
	case engineMetricsMsg:
		if msg.runID != s.runID || s.stopped {
			return s, nil
		}
		if msg.snapshot.GenerationTPS > 0 {
			s.liveTPS = msg.snapshot.GenerationTPS
			s.tpsHistory = appendTPS(s.tpsHistory, s.liveTPS)
		}
		if msg.snapshot.PrefillTPS > 0 {
			s.livePrefillTPS = msg.snapshot.PrefillTPS
		}
		s.liveActive = msg.snapshot.ActiveRequests
		s.liveDeferred = msg.snapshot.QueuedRequests
		s.liveTotalGen = msg.snapshot.GeneratedTokens
		s.liveTotalPrompt = msg.snapshot.PromptTokens
		s.vp.Height = s.computeVPH()
		return s, listenForEngineMetrics(s.runID, s.metricsCh)
	case tea.KeyMsg:
		switch msg.String() {
		case "c":
			s.logs = nil
			s.styledLogs = nil
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
		status = lipgloss.NewStyle().Foreground(t.Warning).Bold(true).Render("◌ shutting down…")
	} else if s.stopped {
		label := "■ stopped"
		if s.forceKilled {
			label = "■ force-killed"
		}
		status = dim.Render(label)
	}
	pid := ""
	if s.process != nil && s.process.PID() != 0 {
		pid = dim.Render(fmt.Sprintf("  pid:%d", s.process.PID()))
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
	if s.runSpec.Launch.Command != "" {
		executor = dim.Render("  executor:") + styleKey.Render(filepath.Base(s.runSpec.Launch.Command))
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
		if spark := renderSparkline(s.tpsHistory, min(24, max(s.width/5, 8))); spark != "" {
			genSegs = append(genSegs, lipgloss.NewStyle().Foreground(t.Primary).Render(spark))
		}
		if s.liveActive > 0 {
			genSegs = append(genSegs, lipgloss.NewStyle().Foreground(t.Success).Render(fmt.Sprintf("● %d active", s.liveActive)))
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
