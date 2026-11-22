package browser

import (
	"context"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// Session is the browser surface one session may use. The engine holds the
// Bridge; a session gets this handle, bound to its own id, so it cannot list or
// rebind clients and cannot act on another session's tab.
type Session struct {
	bridge *Bridge
	id     uuid.UUID
}

// Session returns the handle a session of this id uses.
func (b *Bridge) Session(id uuid.UUID) *Session {
	return &Session{bridge: b, id: id}
}

// Execute runs one browser tool call for this session.
func (s *Session) Execute(ctx context.Context, sessionName string, call contracts.ToolCall) (Output, error) {
	return execute(ctx, s.bridge, s.id, sessionName, call)
}

// Highlight shows where an approved act will land.
func (s *Session) Highlight(ctx context.Context, sessionName string, call contracts.ToolCall) error {
	return highlight(ctx, s.bridge, s.id, sessionName, call)
}

// ClearHighlight removes what Highlight drew.
func (s *Session) ClearHighlight(ctx context.Context, sessionName string, call contracts.ToolCall) error {
	return clearHighlight(ctx, s.bridge, s.id, sessionName, call)
}

// Notify sends an event to this session's side panel.
func (s *Session) Notify(event contracts.EventStreamKind, data any) {
	s.bridge.Notify(s.id, event, data)
}
