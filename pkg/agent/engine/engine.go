// This is the main handling procecss or manager so you can say
package engine

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/google/uuid"
)

const (
	AgentMaxQueuedSessions = 10
)

type AgentComm struct {
	From uuid.UUID
	To   uuid.UUID
	Msg  []byte
}

type AgentComms struct {
	R <-chan AgentComm
	W chan<- AgentComm
}

func FreshAgentComms() *AgentComms {
	return &AgentComms{
		R: make(chan AgentComm),
		W: make(chan AgentComm),
	}
}

type InfaiAgentEngine struct {
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig

	_ *sql.DB
	// Only holds sessions that were enqueued but not yet picked up by Run.
	// "Active" (running) sessions are the goroutines Run spawned from this.
	ActiveQueuedSessions chan *InfaiAgentSession

	stopOnce sync.Once
	stopCh   chan struct{}
	runDone  chan struct{}
	wg       *sync.WaitGroup
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	o := &InfaiAgentEngine{
		bgLogger:             bgLogger,
		engineCfg:            cfg,
		ActiveQueuedSessions: make(chan *InfaiAgentSession, AgentMaxQueuedSessions),
		stopCh:               make(chan struct{}),
		runDone:              make(chan struct{}),
		wg:                   &sync.WaitGroup{},
	}

	return o, nil
}

// Run drains ActiveQueuedSessions and runs each session in its own goroutine.
// It returns when the parent ctx is canceled or when Shutdown closes stopCh.
func (e *InfaiAgentEngine) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer close(e.runDone)
	defer cancel()

	for {
		select {
		case sess, ok := <-e.ActiveQueuedSessions:
			if !ok {
				return nil
			}
			e.wg.Go(func() {
				if err := sess.RunSession(runCtx); err != nil {
					e.bgLogger.ErrorContext(runCtx, "session error", "error", err, "session_id", sess.sessionID)
				}
			})
		case <-e.stopCh:
			// Shutdown requested: stop accepting new sessions. In-flight
			// sessions get runCtx canceled via defer cancel() above.
			return nil
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
}

var (
	ErrSessionCapacityFull = errors.New("session capacity full: maximum active sessions reached")
	ErrEngineShuttingDown  = errors.New("engine is shutting down")
)

// Based on user request like a HTTP or a TUI trigger
func (e *InfaiAgentEngine) CreateSession(ctx context.Context, r io.Reader, w io.Writer) error {
	sess, err := NewSession(r, w, e.bgLogger.WithGroup("session"))
	if err != nil {
		return err
	}

	const enqueueTimeout = 10 * time.Second

	select {
	case e.ActiveQueuedSessions <- sess:
		return nil
	case <-e.stopCh:
		return ErrEngineShuttingDown
	case <-time.After(enqueueTimeout):
		e.bgLogger.ErrorContext(ctx, "session enqueue timed out", "session_id", sess.sessionID)
		return ErrSessionCapacityFull
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *InfaiAgentEngine) ResumeSession(session uuid.UUID, r io.Reader, w io.Writer) {
	// TODO: we need to load from a location into the running memory
}

func (e *InfaiAgentEngine) Shutdown(ctx context.Context) error {
	e.bgLogger.DebugContext(ctx, "Recieved shutdown request")
	// Signal Run to stop draining the channel. Closing stopCh also makes
	// CreateSession fail fast with ErrEngineShuttingDown.
	e.stopOnce.Do(func() {
		close(e.stopCh)
	})

	done := make(chan struct{})
	go func() {
		// Wait for Run to exit first so no more wg.Add can race with wg.Wait.
		<-e.runDone
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
