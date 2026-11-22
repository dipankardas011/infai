// Package browser is the daemon side of the browser extension bridge.
//
// The extension dials out to the daemon over a WebSocket (so the daemon never
// needs to reach into the browser), authenticates with a shared token, and then
// executes actions the daemon sends it, replying with results. Every Chromium
// browser profile that loads the extension is its own client; a session is
// bound to one client by browser_navigate and drives one tab in it.
package browser

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

const (
	// The longest action is wait_for, which the extension lets run for 30s, so
	// the bridge must outlast it or a legitimate "text never appeared" answer
	// would be reported as a bridge timeout instead.
	defaultRequestTimeout = 45 * time.Second
	releaseTimeout        = 5 * time.Second
	shortIDLen            = 8
)

type Option func(*Bridge)

func WithRequestTimeout(d time.Duration) Option {
	return func(b *Bridge) { b.timeout = d }
}

type Status struct {
	Clients  []Client     `json:"clients"`
	Sessions []SessionTab `json:"sessions"`
}

// Client is one connected browser profile.
type Client struct {
	// ID is how the user and the agent name a client: "brave__personal", or
	// "chrome__d1ad7a0c" (instance prefix) until the profile gets a label.
	ID               string    `json:"id"`
	ExtensionVersion string    `json:"extension_version"`
	ConnectedAt      time.Time `json:"connected_at,omitzero"`
	Tabs             int       `json:"tabs"`
}

// SessionTab is one row of the engine's table of which client and tab each
// session drives.
type SessionTab struct {
	SessionID   uuid.UUID `json:"session_id"`
	SessionName string    `json:"session_name"`
	Client      string    `json:"client"`
	TabID       int       `json:"tab_id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	LastAction  string    `json:"last_action"`
	LastUsed    time.Time `json:"last_used"`

	instance string
	// pendingRelease is the instance whose tab this session left during a bind.
	// It is released only after the new client's tab exists, so a bind that
	// fails does not destroy the tab the session was using.
	pendingRelease string
}

// stashedImage is a screenshot uploaded over HTTP, waiting for the tool result
// that will attach it. Keeping the bytes off the WebSocket keeps every reply a
// small frame.
type stashedImage struct {
	data []byte
	at   time.Time
}

const (
	imageTTL  = 2 * time.Minute
	maxImages = 8
)

type Bridge struct {
	log     *slog.Logger
	token   string
	timeout time.Duration

	mu sync.Mutex
	// pendingAuth counts sockets that have not authenticated yet, so anyone who
	// can reach the port cannot hold unbounded goroutines open.
	pendingAuth int
	conns       map[string]*extConn // instance id -> live conn
	closed      bool
	sessions    map[uuid.UUID]*SessionTab
	// images holds screenshots uploaded out of band for the next tool result.
	images map[string]stashedImage
	// orphans are tabs of closed sessions whose client was offline; they are
	// closed when that client reconnects.
	orphans map[string][]uuid.UUID
}

func NewBridge(l *slog.Logger, token string, opts ...Option) *Bridge {
	b := &Bridge{
		log:      l,
		token:    token,
		timeout:  defaultRequestTimeout,
		conns:    make(map[string]*extConn),
		sessions: make(map[uuid.UUID]*SessionTab),
		orphans:  make(map[string][]uuid.UUID),
		images:   make(map[string]stashedImage),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func shortID(instance string) string {
	return instance[:min(shortIDLen, len(instance))]
}

// slug lowercases s and collapses everything that is not a letter or digit
// into single dashes, so a label like "Work (ACME)" becomes "work-acme".
func slug(s string) string {
	return strings.Join(words(s), "-")
}

// baseID is browser__profile, falling back to the instance prefix for an
// unlabelled profile.
func baseID(c *extConn) string {
	browser := slug(c.browser)
	if browser == "" {
		browser = "chromium"
	}
	profile := slug(c.profile)
	if profile == "" {
		profile = shortID(c.instance)
	}
	return browser + "__" + profile
}

// idLocked is baseID, made unique when two profiles of one browser share a
// label.
func (b *Bridge) idLocked(c *extConn) string {
	id := baseID(c)
	for _, other := range b.conns {
		if other != c && other.hello && baseID(other) == id {
			return id + "-" + c.instance[:min(4, len(c.instance))]
		}
	}
	return id
}

// liveLocked returns the authenticated conns, sorted by id so lists read the
// same on every call.
func (b *Bridge) liveLocked() []*extConn {
	live := make([]*extConn, 0, len(b.conns))
	for _, c := range b.conns {
		if c.hello {
			live = append(live, c)
		}
	}
	slices.SortFunc(live, func(x, y *extConn) int {
		return cmp.Or(cmp.Compare(baseID(x), baseID(y)), cmp.Compare(x.instance, y.instance))
	})
	return live
}

func (b *Bridge) clientLocked(c *extConn) Client {
	tabs := 0
	for _, row := range b.sessions {
		if row.instance == c.instance && row.TabID != 0 {
			tabs++
		}
	}
	return Client{
		ID:               b.idLocked(c),
		ExtensionVersion: c.version,
		ConnectedAt:      c.connectedAt,
		Tabs:             tabs,
	}
}

func (b *Bridge) clientsLocked() []Client {
	live := b.liveLocked()
	clients := make([]Client, 0, len(live))
	for _, c := range live {
		clients = append(clients, b.clientLocked(c))
	}
	return clients
}

// Clients lists the connected browser profiles.
func (b *Bridge) Clients() []Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return []Client{}
	}
	return b.clientsLocked()
}

func describeClients(clients []Client) string {
	parts := make([]string, len(clients))
	for i, c := range clients {
		parts[i] = c.ID
	}
	return strings.Join(parts, ", ")
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// resolveLocked picks the one live client a selector names: its id, or words
// that appear in only one id ("brave", "chrome work").
func (b *Bridge) resolveLocked(selector string) (*extConn, error) {
	live := b.liveLocked()
	if len(live) == 0 {
		return nil, ErrNotConnected
	}
	list := func() string { return describeClients(b.clientsLocked()) }
	sel := strings.ToLower(strings.TrimSpace(selector))
	if sel == "" {
		return nil, &ActionError{Code: "client_required", Message: "name the browser to use; connected: " + list()}
	}

	var exact, partial []*extConn
	selWords := words(sel)
	for _, c := range live {
		id := b.idLocked(c)
		if sel == id {
			exact = append(exact, c)
			continue
		}
		nameWords := words(id)
		if len(selWords) > 0 && !slices.ContainsFunc(selWords, func(w string) bool { return !slices.Contains(nameWords, w) }) {
			partial = append(partial, c)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, &ActionError{Code: "client_not_found", Message: fmt.Sprintf("no connected browser matches %q; connected: %s", selector, list())}
	default:
		return nil, &ActionError{Code: "client_ambiguous", Message: fmt.Sprintf("%q matches more than one browser; use the full id. Connected: %s", selector, list())}
	}
}

// Bind points a session at the client a selector names. The tab the session
// leaves is closed only after an action in the new client succeeds (see Do), so
// a bind that never acts cannot destroy the tab the session was using.
func (b *Bridge) Bind(ctx context.Context, sessionID uuid.UUID, sessionName, selector string) (Client, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return Client{}, ErrNotConnected
	}
	c, err := b.resolveLocked(selector)
	if err != nil {
		b.mu.Unlock()
		return Client{}, err
	}
	row := b.sessions[sessionID]
	if row == nil {
		row = &SessionTab{SessionID: sessionID}
		b.sessions[sessionID] = row
	}
	if row.instance != c.instance {
		if row.instance != "" {
			row.pendingRelease = row.instance
		}
		row.TabID, row.URL, row.Title = 0, "", ""
	}
	row.instance = c.instance
	row.Client = b.idLocked(c)
	row.SessionName = sessionName
	info := b.clientLocked(c)
	b.mu.Unlock()
	return info, nil
}

// Do sends action to the client the session is bound to and returns the raw result.
func (b *Bridge) Do(ctx context.Context, sessionID uuid.UUID, sessionName string, action Action, args any) (json.RawMessage, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrNotConnected
	}
	row := b.sessions[sessionID]
	if row == nil || row.instance == "" {
		b.mu.Unlock()
		return nil, &ActionError{Code: "no_tab", Message: "session has no tab yet"}
	}
	c := b.conns[row.instance]
	name := row.Client
	live := c != nil && c.hello
	b.mu.Unlock()
	if !live {
		return nil, &ActionError{Code: "client_disconnected", Message: name + " is not connected"}
	}

	res, err := b.request(ctx, c, sessionID, sessionName, action, args, b.timeout)
	if err != nil {
		return nil, err
	}
	b.recordTab(sessionID, action, res)
	b.releaseLeft(ctx, sessionID, sessionName, row.instance)
	return res, nil
}

// releaseLeft closes the tab a session left behind, now that its new client has
// acted. It keeps the marker when that client is offline, so the tab is closed
// on reconnect instead of leaking.
func (b *Bridge) releaseLeft(ctx context.Context, sessionID uuid.UUID, sessionName, instance string) {
	b.mu.Lock()
	row := b.sessions[sessionID]
	if row == nil || row.pendingRelease == "" || row.pendingRelease == instance {
		b.mu.Unlock()
		return
	}
	prev := row.pendingRelease
	old := b.conns[prev]
	if old != nil && old.hello {
		row.pendingRelease = ""
	}
	b.mu.Unlock()
	if old == nil || !old.hello {
		return
	}
	b.release(ctx, old, sessionID, sessionName)
}

// flushPendingReleases closes tabs left in c's profile while it was offline.
func (b *Bridge) flushPendingReleases(c *extConn) {
	type pending struct {
		sessionID   uuid.UUID
		sessionName string
	}
	var todo []pending
	b.mu.Lock()
	for id, row := range b.sessions {
		if row.pendingRelease == c.instance {
			row.pendingRelease = ""
			todo = append(todo, pending{id, row.SessionName})
		}
	}
	for _, id := range b.orphans[c.instance] {
		todo = append(todo, pending{id, ""})
	}
	delete(b.orphans, c.instance)
	b.mu.Unlock()
	for _, p := range todo {
		b.release(context.Background(), c, p.sessionID, p.sessionName)
	}
}

func (b *Bridge) request(ctx context.Context, c *extConn, sessionID uuid.UUID, sessionName string, action Action, args any, timeout time.Duration) (json.RawMessage, error) {
	rawArgs := json.RawMessage("{}")
	if args != nil {
		var err error
		if rawArgs, err = json.Marshal(args); err != nil {
			return nil, fmt.Errorf("browser %s: encode args: %w", action, err)
		}
	}

	id := uuid.NewString()
	ch := make(chan inbound, 1)
	b.mu.Lock()
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
		Action:      string(action),
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
			return nil, &ActionError{Code: "internal", Message: "extension returned no result for " + string(action)}
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("browser %s: %w", action, ctx.Err())
	case <-timer.C:
		// Say what the daemon saw, so a timeout is diagnosable from the tool
		// result alone: a closed connection, a live one, and how many replies
		// were still expected.
		b.mu.Lock()
		alive := b.conns[c.instance] == c && c.instance != ""
		pending := len(c.pending)
		b.mu.Unlock()
		state := "the connection is still open"
		if !alive {
			state = "the connection had already been replaced"
		}
		return nil, fmt.Errorf("browser %s: no response from extension within %s (%s, %d reply pending)",
			action, timeout, state, pending)
	case <-c.done:
		if cause := c.cause; cause != nil {
			return nil, fmt.Errorf("browser %s: %w", action, cause)
		}
		return nil, fmt.Errorf("browser %s: %w", action, ErrNotConnected)
	}
}

// quietActions are plumbing the user did not ask for; they must not overwrite
// the session's last real action in `infaiw browser status`.
var quietActions = map[Action]bool{ActionHighlight: true, ActionClearHighlight: true}

func (b *Bridge) recordTab(sessionID uuid.UUID, action Action, result json.RawMessage) {
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
		return
	}
	if !quietActions[Action(action)] {
		row.LastAction = string(action)
	}
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
	s := Status{Clients: []Client{}, Sessions: make([]SessionTab, 0, len(b.sessions))}
	if !b.closed {
		s.Clients = b.clientsLocked()
	}
	for _, row := range b.sessions {
		// A live client's id follows its current label.
		if c := b.conns[row.instance]; c != nil && c.hello {
			row.Client = b.idLocked(c)
		}
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
	row := b.sessions[sessionID]
	delete(b.sessions, sessionID)
	var c *extConn
	if row != nil {
		c = b.conns[row.instance]
		if c == nil {
			// Its browser is offline; close the tab when it comes back.
			b.orphans[row.instance] = append(b.orphans[row.instance], sessionID)
		}
	}
	b.mu.Unlock()
	if c == nil {
		return
	}
	b.release(ctx, c, sessionID, row.SessionName)
}

func (b *Bridge) release(ctx context.Context, c *extConn, sessionID uuid.UUID, sessionName string) {
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	_, err := b.request(ctx, c, sessionID, sessionName, ActionReleaseSession, nil, releaseTimeout)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotConnected):
		b.log.Debug("browser session release skipped", "session_id", sessionID, "err", err)
	default:
		b.log.Warn("browser session release failed", "session_id", sessionID, "err", err)
	}
}

// StashImage holds an uploaded screenshot and returns its id.
func (b *Bridge) StashImage(data []byte) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return "", ErrNotConnected
	}
	now := time.Now()
	for id, img := range b.images {
		if now.Sub(img.at) > imageTTL {
			delete(b.images, id)
		}
	}
	if len(b.images) >= maxImages {
		oldest, oldestAt := "", now
		for id, img := range b.images {
			if oldest == "" || img.at.Before(oldestAt) {
				oldest, oldestAt = id, img.at
			}
		}
		delete(b.images, oldest)
	}
	id := uuid.NewString()
	b.images[id] = stashedImage{data: data, at: now}
	return id, nil
}

// TakeImage hands the uploaded screenshot to the tool result, once.
func (b *Bridge) TakeImage(id string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	img, ok := b.images[id]
	if !ok {
		return nil, false
	}
	delete(b.images, id)
	return img.data, true
}

// Notify pushes a fire-and-forget event to the side panel of the session's
// client. Only a session that has never been bound goes to every client (the
// approval of its first browser_navigate). A bound session whose client is
// offline is dropped: its approval carries the tool arguments, so showing it in
// another profile would leak, and answering it there would fail.
func (b *Bridge) Notify(sessionID uuid.UUID, event contracts.EventStreamKind, data any) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	var targets []*extConn
	bound := false
	if row := b.sessions[sessionID]; row != nil && row.instance != "" {
		bound = true
		if c := b.conns[row.instance]; c != nil && c.hello {
			targets = []*extConn{c}
		}
	}
	if !bound {
		targets = b.liveLocked()
	}
	b.mu.Unlock()
	if len(targets) == 0 {
		b.log.Debug("browser event has no target", "event", event, "session_id", sessionID)
	}

	for _, c := range targets {
		if err := c.write(eventFrame{Type: "event", Event: string(event), Data: data}); err != nil {
			b.log.Warn("browser event send failed", "event", event, "err", err)
		}
	}
}

// broadcastIDs tells every connected panel the id the daemon gave it. Ids
// depend on the other connected profiles (a shared label adds a suffix), so any
// connect or disconnect can change one, and the panel must not render its own
// guess.
func (b *Bridge) broadcastIDs() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	live := b.liveLocked()
	type target struct {
		c  *extConn
		id string
	}
	targets := make([]target, 0, len(live))
	for _, c := range live {
		targets = append(targets, target{c, b.idLocked(c)})
	}
	b.mu.Unlock()

	for _, t := range targets {
		if err := t.c.write(idFrame{Type: "client_id", ID: t.id}); err != nil {
			b.log.Debug("browser client id send failed", "id", t.id, "err", err)
		}
	}
}

func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	conns := b.conns
	b.conns = make(map[string]*extConn)
	b.images = make(map[string]stashedImage)
	b.mu.Unlock()

	for _, c := range conns {
		c.close()
	}
	return nil
}
