package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	read  io.Reader
	write io.Writer

	model contracts.InfaiModelAdaptor

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentComms        // By this when N no of children can send comms with engine and even the

	baseAgentId uuid.UUID

	Agents map[uuid.UUID]*agent.Agent
}

// model is chosen per-session (the model a session runs is a property of the
// conversation, not the engine). Hardcoded to a local OpenAI-compatible
// endpoint for now; the request payload will drive selection later.
func NewSession(r io.Reader, w io.Writer, l *slog.Logger) (*InfaiAgentSession, error) {
	o := &InfaiAgentSession{
		l:             l,
		read:          r,
		write:         w,
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*agent.Agent),
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

	firstAgent.SetModel(models.NewOpenAICompatableAPI("http://0.0.0.0:8000/v1", "local-model", ""))

	// Primitive: the first line read from the session input is the user
	// prompt. Proper request parsing/session resume comes later.
	userPrompt, err := bufio.NewReader(e.read).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	userPrompt = strings.TrimSpace(userPrompt)

	systemPrompt, err := GetBasicSystemPrompt(nil, nil)
	if err != nil {
		return err
	}

	if _, err := firstAgent.Invoke(ctx, systemPrompt, userPrompt); err != nil {
		e.l.ErrorContext(ctx, "agent invoke failed", "session_id", e.sessionID, "error", err)
		return err
	}

	return nil
}

func (e *InfaiAgentSession) registerNewParentAgent() (*agent.Agent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	e.agentMapping[id] = ds.NewSet[uuid.UUID]()
	e.runtime_comms[id] = FreshAgentComms()
	e.Agents[id], err = agent.NewAgent(id,
		agent.WithMaxTurns(100),
		agent.WithTurnHook(func(m contracts.ChatMessage) {
			fmt.Fprintf(e.write, "[%s] %s\n", m.Role, m.Text())
		}),
	)
	if err != nil {
		return nil, err
	}

	return e.Agents[id], nil
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
