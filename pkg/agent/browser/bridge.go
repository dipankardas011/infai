// Package browser is the daemon side of the browser extension bridge.
//
// The Chrome extension dials out to the daemon over a WebSocket (so the
// daemon never needs to reach into the browser), authenticates with a shared
// token, and then executes actions the daemon sends it, replying with results.
// The Bridge also tracks which tab each agent session is driving.
package browser

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// The longest action is wait_for, which the extension lets run for 30s, so
	// the bridge must outlast it or a legitimate "text never appeared" answer
	// would be reported as a bridge timeout instead.
	defaultRequestTimeout = 45 * time.Second
	releaseTimeout        = 5 * time.Second
	releaseAction         = "release_session"
)

type Option func(*Bridge)

func WithRequestTimeout(d time.Duration) Option {
	return func(b *Bridge) { b.timeout = d }
}

type Status struct {
	Connected        bool         `json:"connected"`
	Profile          string       `json:"profile"`
	ExtensionVersion string       `json:"extension_version"`
	ConnectedAt      time.Time    `json:"connected_at,omitzero"`
	Sessions         []SessionTab `json:"sessions"`
}

// SessionTab is one row of the engine's table of which tab each session drives.
type SessionTab struct {
	SessionID   uuid.UUID `json:"session_id"`
	SessionName string    `json:"session_name"`
	TabID       int       `json:"tab_id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	LastAction  string    `json:"last_action"`
	LastUsed    time.Time `json:"last_used"`
}

type Bridge struct {
	log     *slog.Logger
	token   string
	timeout time.Duration

	mu       sync.Mutex
	conn     *extConn
	closed   bool
	sessions map[uuid.UUID]*SessionTab
}

func NewBridge(l *slog.Logger, token string, opts ...Option) *Bridge {
	b := &Bridge{
		log:      l,
		token:    token,
		timeout:  defaultRequestTimeout,
		sessions: make(map[uuid.UUID]*SessionTab),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Do sends action to the extension on behalf of a session and returns the raw result.
func (b *Bridge) Do(ctx context.Context, sessionID uuid.UUID, sessionName, action string, args any) (json.RawMessage, error) {
	res, err := b.do(ctx, sessionID, sessionName, action, args, b.timeout)
	if err != nil {
		return nil, err
	}
	if action != releaseAction {
		b.recordTab(sessionID, sessionName, action, res)
	}
	return res, nil
}

func (b *Bridge) do(ctx context.Context, sessionID uuid.UUID, sessionName, action string, args any, timeout time.Duration) (json.RawMessage, error) {
	rawArgs := json.RawMessage("{}")
	if args != nil {
		var err error
		if rawArgs, err = json.Marshal(args); err != nil {
			return nil, fmt.Errorf("browser %s: encode args: %w", action, err)
		}
	}

	b.mu.Lock()
	c := b.conn
	if b.closed || c == nil || !c.hello {
		b.mu.Unlock()
		return nil, ErrNotConnected
	}
	id := uuid.NewString()
	ch := make(chan inbound, 1)
	c.pending[id] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(c.pending, id)
		b.mu.Unlock()
	}()

	req := requestFrame{
		Type:        "request",
		ID:          id,
		SessionID:   sessionID,
		SessionName: sessionName,
		Action:      action,
		Args:        rawArgs,
	}
	if err := c.write(req); err != nil {
		// A failed write leaves the frame stream in an unknown state; drop the conn.
		c.close()
		return nil, fmt.Errorf("browser %s: %w: %v", action, ErrNotConnected, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case m := <-ch:
		if !m.OK {
			if m.Error == nil {
				return nil, &ActionError{Code: "internal", Message: "extension reported failure without details"}
			}
			return nil, &ActionError{Code: m.Error.Code, Message: m.Error.Message}
		}
		if len(m.Result) == 0 || string(m.Result) == "null" {
			return nil, &ActionError{Code: "internal", Message: "extension returned no result for " + action}
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("browser %s: %w", action, ctx.Err())
	case <-timer.C:
		return nil, fmt.Errorf("browser %s: no response from extension within %s", action, timeout)
	case <-c.done:
		return nil, fmt.Errorf("browser %s: %w", action, ErrNotConnected)
	}
}

func (b *Bridge) recordTab(sessionID uuid.UUID, sessionName, action string, result json.RawMessage) {
	var info struct {
		TabID *int    `json:"tab_id"`
		URL   *string `json:"url"`
		Title *string `json:"title"`
	}
	// Results are action-specific and may not be objects; missing fields are fine.
	_ = json.Unmarshal(result, &info)

	b.mu.Lock()
	defer b.mu.Unlock()
	row := b.sessions[sessionID]
	if row == nil {
		row = &SessionTab{SessionID: sessionID}
		b.sessions[sessionID] = row
	}
	row.SessionName = sessionName
	row.LastAction = action
	row.LastUsed = time.Now()
	if info.TabID != nil {
		row.TabID = *info.TabID
	}
	if info.URL != nil {
		row.URL = *info.URL
	}
	if info.Title != nil {
		row.Title = *info.Title
	}
}

func (b *Bridge) Status() Status {
	b.mu.Lock()
	s := Status{Sessions: make([]SessionTab, 0, len(b.sessions))}
	if c := b.conn; c != nil && c.hello && !b.closed {
		s.Connected = true
		s.Profile = c.profile
		s.ExtensionVersion = c.version
		s.ConnectedAt = c.connectedAt
	}
	for _, row := range b.sessions {
		s.Sessions = append(s.Sessions, *row)
	}
	b.mu.Unlock()

	slices.SortFunc(s.Sessions, func(a, b SessionTab) int {
		return cmp.Compare(b.LastUsed.UnixNano(), a.LastUsed.UnixNano())
	})
	return s
}

func (b *Bridge) Release(ctx context.Context, sessionID uuid.UUID) {
	b.mu.Lock()
	var name string
	if row := b.sessions[sessionID]; row != nil {
		name = row.SessionName
	}
	delete(b.sessions, sessionID)
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	_, err := b.do(ctx, sessionID, name, releaseAction, nil, releaseTimeout)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotConnected):
		b.log.Debug("browser session release skipped", "session_id", sessionID, "err", err)
	default:
		b.log.Warn("browser session release failed", "session_id", sessionID, "err", err)
	}
}

// Notify pushes a fire-and-forget event to the extension's side panel.
func (b *Bridge) Notify(event string, data any) {
	b.mu.Lock()
	c := b.conn
	ok := c != nil && c.hello && !b.closed
	b.mu.Unlock()
	if !ok {
		return
	}
	if err := c.write(eventFrame{Type: "event", Event: event, Data: data}); err != nil {
		b.log.Warn("browser event send failed", "event", event, "err", err)
	}
}

func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	c := b.conn
	b.conn = nil
	b.mu.Unlock()

	if c != nil {
		c.close()
	}
	return nil
}
