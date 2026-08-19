package engine

import (
	"context"
	"io"
	"log/slog"

	"github.com/dipankardas011/infai/pkg/agent/loop"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	read  io.Reader
	write io.Writer

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentComms        // By this when N no of children can send comms with engine and even the

	baseAgentId uuid.UUID

	Agents map[uuid.UUID]*loop.Agent
}

func NewSession(r io.Reader, w io.Writer, l *slog.Logger) (*InfaiAgentSession, error) {
	o := &InfaiAgentSession{
		l:             l,
		read:          r,
		write:         w,
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*loop.Agent),
	}

	if v, err := uuid.NewV7(); err != nil {
		return nil, err
	} else {
		o.sessionID = v
	}

	return o, nil
}

func (e *InfaiAgentSession) RunSession(ctx context.Context) error {

	e.l.InfoContext(ctx, "Session started", "session_id", e.sessionID)
	defer e.l.InfoContext(ctx, "Session ended", "session_id", e.sessionID)

	// for now assumption is always the first node is the parentagent
	firstAgent, err := e.registerNewParentAgent()
	if err != nil {
		return err
	}

	firstAgent.Invoke()

	return nil
}

func (e *InfaiAgentSession) registerNewParentAgent() (*loop.Agent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	e.agentMapping[id] = ds.NewSet[uuid.UUID]()
	e.runtime_comms[id] = FreshAgentComms()
	e.Agents[id], err = loop.NewAgent(id, loop.WithMaxTurns(100))
	return e.Agents[id], err
}

// func (e *InfaiAgentEngine) spawnSubAgent(
// 	parentId uuid.UUID,
// 	parentComms *AgentComms,
// ) error {
// 	// TODO: we need to handle the concurrency here
// 	id, err := uuid.NewV7()
// 	if err != nil {
// 		return err
// 	}
// 	e.agentMapping[parentId].Add(id)
// 	e.agentMapping[id] = ds.NewSet[uuid.UUID]()
// 	e.runtime_comms[id] = parentComms
//
// 	return nil
// }
