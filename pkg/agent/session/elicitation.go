package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pendingElicitation struct {
	request    contracts.ElicitationRequest
	conclusion chan contracts.ElicitationConclusion
}

// MCPElicit asks the user a question on behalf of an MCP server mid-call and blocks
// until an answer arrives or the turn is canceled. It mirrors performHITL.
func (s *InfaiAgentSession) MCPElicit(ctx context.Context, server string, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	if params.Mode != "" && params.Mode != "form" {
		// Only "form" has a surface in the harness; a "url" elicitation is
		// declined rather than left waiting for a page we cannot show.
		return &mcp.ElicitResult{Action: contracts.ElicitationDecline}, nil
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	request := contracts.ElicitationRequest{
		ID:        id,
		SessionID: s.meta.ID,
		Server:    server,
		Mode:      params.Mode,
		Message:   params.Message,
		CreatedAt: time.Now().UTC(),
	}
	if params.RequestedSchema != nil {
		schema, err := json.Marshal(params.RequestedSchema)
		if err != nil {
			return nil, err
		}
		request.Schema = schema
	}

	pending := &pendingElicitation{
		request:    request,
		conclusion: make(chan contracts.ElicitationConclusion, 1),
	}

	s.mu.Lock()
	if s.pendingElicitation != nil {
		s.mu.Unlock()
		return nil, errors.New("another elicitation is already pending")
	}
	pending.request.SessionName = s.meta.Name
	request = pending.request
	s.pendingElicitation = pending
	s.mu.Unlock()

	s.publish(contracts.EventStream{Kind: contracts.EventElicitationRequested, Timestamp: time.Now().UTC(), Elicitation: &request})

	s.l.InfoContext(ctx, "elicitation requested",
		"elicitation_id", request.ID,
		"session_id", request.SessionID,
		"server", request.Server,
	)

	var conclusion contracts.ElicitationConclusion
	select {
	case conclusion = <-pending.conclusion:
	case <-ctx.Done():
		s.mu.Lock()
		if s.pendingElicitation != pending {
			s.mu.Unlock()
			conclusion = <-pending.conclusion
			break
		}
		s.pendingElicitation = nil
		s.mu.Unlock()
		// A canceled turn is not a protocol failure: the server is told the
		// elicitation was canceled so it can unwind rather than retry.
		conclusion = contracts.ElicitationConclusion{ReqID: request.ID, Action: contracts.ElicitationCancel}
	}

	s.mu.Lock()
	if s.pendingElicitation == pending {
		s.pendingElicitation = nil
	}
	s.mu.Unlock()

	s.publish(contracts.EventStream{
		Kind:              contracts.EventElicitationResolved,
		Timestamp:         time.Now().UTC(),
		Elicitation:       &request,
		ElicitationResult: &conclusion,
	})

	s.l.InfoContext(ctx, "elicitation resolved",
		"elicitation_id", request.ID,
		"action", conclusion.Action,
	)

	result := &mcp.ElicitResult{Action: conclusion.Action}
	if conclusion.Action == contracts.ElicitationAccept {
		result.Content = conclusion.Content
	}
	return result, nil
}

func (s *InfaiAgentSession) ResolveElicitation(reqID uuid.UUID, conclusion contracts.ElicitationConclusion) error {
	s.mu.Lock()

	if s.pendingElicitation == nil || s.pendingElicitation.request.ID != reqID {
		s.mu.Unlock()
		return errors.New("elicitation not found or already resolved")
	}

	if conclusion.Action != contracts.ElicitationAccept && conclusion.Action != contracts.ElicitationDecline && conclusion.Action != contracts.ElicitationCancel {
		s.mu.Unlock()
		return errors.New("invalid elicitation action")
	}

	pending := s.pendingElicitation
	s.pendingElicitation = nil
	s.mu.Unlock()

	// The handler publishes the resolution event once it observes the
	// conclusion, mirroring how an approval resolution is released here.
	pending.conclusion <- contracts.ElicitationConclusion{ReqID: reqID, Action: conclusion.Action, Content: conclusion.Content}

	return nil
}
