package store

import (
	"errors"
	"sync"
)

// ErrNotFound is returned when a requested session does not exist.
var ErrNotFound = errors.New("store: session not found")

// SessionEventSink consumes live records from a session.
type SessionEventSink func(Record) error

// SessionEventHub broadcasts live records to session subscribers. Timeline
// owns durable history; the hub exists only for live transports such as SSE.
type SessionEventHub struct {
	mu    sync.Mutex
	sinks map[int]SessionEventSink
	next  int
}

func NewSessionEventHub() *SessionEventHub {
	return &SessionEventHub{sinks: make(map[int]SessionEventSink)}
}

// Subscribe registers a live consumer and returns an unsubscribe function.
func (h *SessionEventHub) Subscribe(fn SessionEventSink) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.next
	h.next++
	h.sinks[id] = fn
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.sinks, id)
	}
}

// Publish sends a live record to every subscriber. Subscriber errors are
// isolated so one disconnected transport cannot block another.
func (h *SessionEventHub) Publish(rec Record) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// TODO: What is this?
	for _, sink := range h.sinks {
		_ = sink(rec)
	}
}

func (h *SessionEventHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sinks = nil
}
