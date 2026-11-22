package session

import (
	"context"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/browser"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// browserCallTimeout bounds the cosmetic highlight calls made around an
// approval, so a slow extension never holds up the decision itself.
const browserCallTimeout = 5 * time.Second

func (s *InfaiAgentSession) sessionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta.Name
}

func (s *InfaiAgentSession) browserApprovalOpened(ctx context.Context, request contracts.ApprovalRequest) {
	if s.browser == nil || !browser.IsTool(request.ToolCall.Function.Name) {
		return
	}
	s.browser.Notify(s.meta.ID, "approval_requested", request)

	ctx, cancel := context.WithTimeout(ctx, browserCallTimeout)
	defer cancel()
	if err := browser.Highlight(ctx, s.browser, s.meta.ID, request.SessionName, request.ToolCall); err != nil {
		s.l.Debug("browser highlight failed", "session_id", s.meta.ID, "error", err)
	}
}

func (s *InfaiAgentSession) browserApprovalClosed(request contracts.ApprovalRequest, decision contracts.ApprovalConclusion) {
	if s.browser == nil || !browser.IsTool(request.ToolCall.Function.Name) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), browserCallTimeout)
	defer cancel()
	if err := browser.ClearHighlight(ctx, s.browser, s.meta.ID, request.SessionName, request.ToolCall); err != nil {
		s.l.Debug("browser clear highlight failed", "session_id", s.meta.ID, "error", err)
	}
	s.browser.Notify(s.meta.ID, "approval_resolved", map[string]any{
		"req_id":     request.ID,
		"session_id": request.SessionID,
		"decision":   decision.Decision,
	})
}
