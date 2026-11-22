package browser

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/vision"
	"github.com/google/uuid"
	"golang.org/x/net/websocket"
)

const (
	writeTimeout = 10 * time.Second
	// The extension pings every 20s, so a minute of silence means the socket is dead.
	idleTimeout = 60 * time.Second
	// An unauthenticated socket may only wait this long for its hello.
	authTimeout = 10 * time.Second
	// Sockets that have not sent hello yet. Bounds what an unauthenticated
	// client can hold open.
	maxPendingAuth = 8
	// A screenshot reply is a base64 image: at most vision.MaxImageBytes (5 MiB)
	// of bytes, so ~7 MiB on the wire. The cap reuses the engine's per-turn
	// image total, so there is one budget for images, not two.
	maxPayloadBytes = vision.MaxTotalBytes
)

// inbound is any frame sent by the extension; fields are populated per Type.
// isOversizeFrame reports whether a read failed because the peer's frame was
// larger than Config.MaxPayloadBytes (x/net/websocket words it per version).
func isOversizeFrame(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "frame payload size exceeds") ||
		strings.Contains(msg, "payload size limit") ||
		strings.Contains(msg, "max payload")
}

type inbound struct {
	Type             string          `json:"type"`
	Token            string          `json:"token"`
	ID               string          `json:"id"`
	Profile          string          `json:"profile"`
	Instance         string          `json:"instance"`
	Browser          string          `json:"browser"`
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

// idFrame tells a panel the client id the daemon assigned it.
type idFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type extConn struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once

	// cause explains why the socket closed, when the daemon closed it for a
	// reason the caller can act on (e.g. an oversized reply).
	cause       error
	pending     map[string]chan inbound
	hello       bool
	profile     string
	instance    string
	browser     string
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

// failWith records why the daemon is closing this conn, then closes it. Waiters
// read cause after done is closed, so the write happens-before the read.
func (c *extConn) failWith(cause error) {
	c.cause = cause
	c.close()
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

// ServeHTTP is the WebSocket endpoint the extension dials; it authenticates
// with the token in its first (hello) frame.
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
	ws.MaxPayloadBytes = maxPayloadBytes

	b.mu.Lock()
	if b.closed || b.pendingAuth >= maxPendingAuth {
		b.mu.Unlock()
		_ = ws.Close()
		return
	}
	b.pendingAuth++
	b.mu.Unlock()

	c := newExtConn(ws)
	b.readLoop(c)
	c.close()

	b.mu.Lock()
	b.pendingAuth--
	wasLive := c.instance != "" && b.conns[c.instance] == c
	if wasLive {
		delete(b.conns, c.instance)
	}
	name := baseID(c)
	b.mu.Unlock()
	if wasLive {
		b.log.Info("browser client disconnected", "client", name)
		b.broadcastIDs()
	}
}

// claim registers an authenticated conn under its extension instance id: one
// id per browser profile, stable across reconnects. A reconnect from the same
// profile replaces its own stale socket (e.g. after sleep); other profiles and
// browsers are separate clients and never evict each other.
func (b *Bridge) claim(c *extConn, instance string) bool {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return false
	}
	c.instance = instance
	old := b.conns[instance]
	b.conns[instance] = c
	b.mu.Unlock()

	if old != nil {
		b.log.Info("browser client reconnected, replacing its previous connection", "id", shortID(instance))
		old.close()
	}
	return true
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
			if isOversizeFrame(err) {
				b.log.Warn("browser client reply exceeded the frame limit",
					"limit_bytes", maxPayloadBytes, "err", err)
				c.failWith(&ActionError{
					Code: "reply_too_large",
					Message: fmt.Sprintf("the extension's reply exceeded the %d MiB frame limit; "+
						"capture a smaller screenshot (the daemon scales it to 1280px, so this usually means a huge page region)",
						maxPayloadBytes>>20),
				})
				return
			}
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
			if m.Instance == "" {
				b.log.Warn("browser extension rejected: hello has no instance id; reload the extension")
				return
			}
			if !b.claim(c, m.Instance) {
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
		c.browser = m.Browser
		c.version = m.ExtensionVersion
		if c.connectedAt.IsZero() {
			c.connectedAt = time.Now()
		}
		b.mu.Unlock()
		if first {
			b.log.Info("browser client connected", "browser", m.Browser, "profile", m.Profile, "id", shortID(m.Instance), "version", m.ExtensionVersion)
			b.broadcastIDs()
			go b.flushPendingReleases(c)
		} else {
			// The side panel re-sends hello when the profile label changes.
			b.log.Debug("browser extension hello refreshed", "profile", m.Profile)
			b.broadcastIDs()
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
