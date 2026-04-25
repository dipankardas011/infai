package tui

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// Tea messages for server I/O.
type logLineMsg string
type serverExitMsg struct{ err error }

func listenForLog(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return serverExitMsg{}
		}
		return logLineMsg(line)
	}
}

const maxLogLines = 10000

// ServerModel is screen 5 — shows live llama-server output.
type ServerModel struct {
	cmd         *exec.Cmd
	logCh       chan string
	logs        []string
	vp          viewport.Model
	profileName string
	modelName   string
	port        int
	stopped     bool
	stopping    bool
	exitErr     error
	width       int
	height      int
	initialized bool
}

// NewServerModel starts the server process and returns the model + initial listen cmd.
func NewServerModel(args []string, profileName, modelName string, port, w, h int) (ServerModel, tea.Cmd, error) {
	cmd := exec.Command(args[0], args[1:]...)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ServerModel{}, nil, err
	}

	logCh := make(chan string, 256)

	// goroutine: read lines → channel
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			logCh <- stripAnsi(sc.Text())
		}
		close(logCh)
	}()

	// goroutine: wait for exit → close pipe
	go func() {
		cmd.Wait()
		pw.Close()
	}()

	vpH := h - 6
	if vpH < 5 {
		vpH = 5
	}
	vp := viewport.New(w-4, vpH)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary)

	m := ServerModel{
		cmd:         cmd,
		logCh:       logCh,
		vp:          vp,
		profileName: profileName,
		modelName:   modelName,
		port:        port,
		width:       w,
		height:      h,
		initialized: true,
	}
	return m, listenForLog(logCh), nil
}

func (s ServerModel) HandleLogLine(line string) (ServerModel, tea.Cmd) {
	s.logs = append(s.logs, line)
	if len(s.logs) > maxLogLines {
		s.logs = s.logs[len(s.logs)-maxLogLines:]
	}
	atBottom := s.vp.AtBottom()
	s.vp.SetContent(strings.Join(s.logs, "\n"))
	if atBottom {
		s.vp.GotoBottom()
	}
	return s, listenForLog(s.logCh)
}

func (s ServerModel) SetExited(err error) ServerModel {
	s.stopped = true
	s.stopping = false
	s.exitErr = err
	return s
}

func (s ServerModel) Stop() ServerModel {
	if s.cmd != nil && s.cmd.Process != nil && !s.stopped {
		s.stopping = true
		// Kill the whole process group to also terminate child processes.
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	}
	return s
}

func (s ServerModel) SetSize(w, h int) ServerModel {
	if !s.initialized {
		return s
	}
	s.width = w
	s.height = h
	vpH := h - 6
	if vpH < 5 {
		vpH = 5
	}
	s.vp.Width = w - 4
	s.vp.Height = vpH
	return s
}

func (s ServerModel) Update(msg tea.Msg) (ServerModel, tea.Cmd) {
	switch msg := msg.(type) {
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

	// Header
	status := lipgloss.NewStyle().Foreground(t.Success).Bold(true).Render("● running")
	if s.stopping {
		status = lipgloss.NewStyle().Foreground(t.Error).Render("◌ stopping…")
	} else if s.stopped {
		status = lipgloss.NewStyle().Foreground(t.Muted).Render("■ stopped")
	}
	pid := ""
	if s.cmd != nil && s.cmd.Process != nil {
		pid = styleMuted.Render(fmt.Sprintf("  pid:%d", s.cmd.Process.Pid))
	}
	portStr := styleKey.Render(fmt.Sprintf("  port:%d", s.port))
	header := styleTitle.Render(s.profileName) + "  " + status + pid + portStr +
		"\n" + styleMuted.Render("  model: "+s.modelName)

	// Log viewport
	logView := s.vp.View()

	// Footer
	var help string
	if s.stopped {
		exitStatus := styleSuccess.Render("exited cleanly")
		if s.exitErr != nil {
			exitStatus = styleError.Render("error: " + s.exitErr.Error())
		}
		help = exitStatus + "\n" + styleHelp.Render("esc: back to profiles")
	} else {
		help = styleHelp.Render("s: stop server  c: clear logs  esc: stop & back  ↑↓/pgup/pgdn: scroll logs")
	}

	return header + "\n\n" + logView + "\n" + help
}
