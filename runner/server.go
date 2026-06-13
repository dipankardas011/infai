package runner

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// ServerProcess owns llama-server process execution and I/O.
// It contains no Bubble Tea/UI code and is safe for TUI or future non-TUI callers.
type ServerProcess struct {
	cmd       *exec.Cmd
	args      []string
	logCh     chan string
	exitCh    chan error
	startedAt time.Time
}

func StartServer(args []string) (*ServerProcess, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("missing executable")
	}

	cmd := exec.Command(args[0], args[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, err
	}

	p := &ServerProcess{
		cmd:       cmd,
		args:      append([]string(nil), args...),
		logCh:     make(chan string, 256),
		exitCh:    make(chan error, 1),
		startedAt: time.Now(),
	}

	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			p.logCh <- stripANSI(sc.Text())
		}
		close(p.logCh)
	}()

	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		p.exitCh <- err
	}()

	return p, nil
}

func (p *ServerProcess) Args() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.args...)
}

func (p *ServerProcess) Logs() <-chan string {
	if p == nil {
		return nil
	}
	return p.logCh
}

func (p *ServerProcess) Exits() <-chan error {
	if p == nil {
		return nil
	}
	return p.exitCh
}

func (p *ServerProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *ServerProcess) StartedAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.startedAt
}

func (p *ServerProcess) Stop() error {
	pid := p.PID()
	if pid == 0 {
		return fmt.Errorf("process not running")
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func (p *ServerProcess) ForceKill() error {
	pid := p.PID()
	if pid == 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
