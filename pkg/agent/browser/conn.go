package browser

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
)

const (
	writeTimeout = 10 * time.Second
	// The extension pings every 20s, so a minute of silence means the socket is dead.
	idleTimeout = 60 * time.Second
	// An unauthenticated socket may only wait this long for its hello.
	authTimeout = 10 * time.Second
)

// inbound is any frame sent by the extension; fields are populated per Type.
type inbound struct {
	Type             string          `json:"type"`
	Token            string          `json:"token"`
	ID               string          `json:"id"`
	Profile          string          `json:"profile"`
	ExtensionVersion string          `json:"extension_version"`
	OK               bool            `json:"ok"`
	Result           json.RawMessage `json:"result"`
	Error            *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type requestFrame struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	SessionID   uuid.UUID       `json:"session_id"`
	SessionName string          `json:"session_name"`
	Action      string          `json:"action"`
	Args        json.RawMessage `json:"args"`
}

type eventFrame struct {
	Type  string `json:"type"`
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type typeFrame struct {
	Type string `json:"type"`
}

type extConn struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once

	pending     map[string]chan inbound
	hello       bool
	profile     string
	version     string
	connectedAt time.Time
}

func newExtConn(ws *websocket.Conn) *extConn {
	return &extConn{
		ws:      ws,
		done:    make(chan struct{}),
		pending: make(map[string]chan inbound),
	}
}

func (c *extConn) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return websocket.JSON.Send(c.ws, v)
}

// close fails every waiter on this conn (they select on done) and tears down the socket.
func (c *extConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		// Bound the close frame write in case a writer is stuck on a half-dead socket.
		_ = c.ws.SetWriteDeadline(time.Now().Add(time.Second))
		_ = c.ws.Close()
	})
}

// ServeHTTP is the WebSocket endpoint the extension dials with ?token=<token>.
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	websocket.Server{Handshake: b.handshake, Handler: b.handle}.ServeHTTP(w, r)
}

// handshake replaces the default origin check, which would reject the
// chrome-extension:// origin. Authentication cannot happen here: a browser
// cannot set headers on a WebSocket, and a token in the query string would be
// written to every proxy access log. The first frame carries it instead.
func (b *Bridge) handshake(_ *websocket.Config, _ *http.Request) error {
	if b.token == "" {
		return errors.New("bridge has no token")
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return errors.New("bridge closed")
	}
	return nil
}

func (b *Bridge) handle(ws *websocket.Conn) {
	c := newExtConn(ws)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		c.close()
		return
	}
	old := b.conn
	b.conn = c
	b.mu.Unlock()

	if old != nil {
		b.log.Info("browser extension reconnected, replacing previous connection")
		old.close()
	}

	b.readLoop(c)

	c.close()
	b.mu.Lock()
	if b.conn == c {
		b.conn = nil
	}
	b.mu.Unlock()
	b.log.Info("browser extension disconnected")
}

func (b *Bridge) readLoop(c *extConn) {
	authed := false
	for {
		deadline := idleTimeout
		if !authed {
			deadline = authTimeout
		}
		if err := c.ws.SetReadDeadline(time.Now().Add(deadline)); err != nil {
			return
		}
		var data []byte
		if err := websocket.Message.Receive(c.ws, &data); err != nil {
			select {
			case <-c.done:
			default:
				b.log.Debug("browser extension read failed", "err", err)
			}
			return
		}
		var m inbound
		if err := json.Unmarshal(data, &m); err != nil {
			b.log.Debug("browser extension sent invalid frame", "err", err)
			continue
		}
		if !authed {
			if m.Type != "hello" || subtle.ConstantTimeCompare([]byte(m.Token), []byte(b.token)) != 1 {
				b.log.Warn("browser extension rejected: bad or missing token in hello")
				return
			}
			authed = true
		}
		b.dispatch(c, m)
	}
}

func (b *Bridge) dispatch(c *extConn, m inbound) {
	switch m.Type {
	case "hello":
		b.mu.Lock()
		first := !c.hello
		c.hello = true
		c.profile = m.Profile
		c.version = m.ExtensionVersion
		if c.connectedAt.IsZero() {
			c.connectedAt = time.Now()
		}
		b.mu.Unlock()
		if first {
			b.log.Info("browser extension connected", "profile", m.Profile, "version", m.ExtensionVersion)
		} else {
			// The side panel re-sends hello when the profile label changes.
			b.log.Debug("browser extension hello refreshed", "profile", m.Profile)
		}
	case "ping":
		if err := c.write(typeFrame{Type: "pong"}); err != nil {
			b.log.Debug("browser extension pong failed", "err", err)
		}
	case "response":
		b.mu.Lock()
		ch := c.pending[m.ID]
		b.mu.Unlock()
		if ch == nil {
			b.log.Debug("browser extension response for unknown request", "id", m.ID)
			return
		}
		select {
		case ch <- m:
		default: // duplicate response; the first one already won
		}
	default:
		b.log.Debug("browser extension sent unknown frame type", "type", m.Type)
	}
}
