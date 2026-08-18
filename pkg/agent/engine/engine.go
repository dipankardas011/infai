// This is the main handling procecss or manager so you can say
package engine

import (
	"context"
	"log/slog"

	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/loop"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

type AgentComm struct {
	From uuid.UUID
	To   uuid.UUID
	Msg  []byte
}

type AgentComms struct {
	R <-chan AgentComm
	W chan<- AgentComm
}

func FreshAgentComms() *AgentComms {
	return &AgentComms{
		R: make(chan AgentComm),
		W: make(chan AgentComm),
	}
}

// TODO: we would need to handle to resume a session where we need to load the agentMapping and sessionId as the first process.
type InfaiAgentEngine struct {
	sessionID uuid.UUID
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig

	// Need to handle N number of agents as go routine so for that case a better place ig is goroutune manager or a workerPool manager like with one agent only when launch and when needed we can call and when there is a specific <if_cond{subagent}> then we can branch it in some sort by tell engine to register Another agent given a UUIDv7 of a agent it will check and register based on resource constraints of the harness
	// for communication we can use a channel a RWMap which we can use for retrival of neccessary pipe for sharing info

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentComms        // By this when N no of children can send comms with engine and even the

	Agents map[uuid.UUID]*loop.Agent
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	o := &InfaiAgentEngine{
		bgLogger:      bgLogger,
		engineCfg:     cfg,
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentComms),
		Agents:        make(map[uuid.UUID]*loop.Agent),
	}

	if v, err := uuid.NewV7(); err != nil {
		o.sessionID = v
	}

	return o, nil
}

func (e *InfaiAgentEngine) Run() error {
	e.bgLogger.Info("Session started", "session_id", e.sessionID)
	defer e.bgLogger.Info("Session ended", "session_id", e.sessionID)

	// for now assumption is always the first node is the parentagent
	firstAgent, err := e.registerNewParentAgent()
	if err != nil {
		return err
	}

	firstAgent.Invoke()

	return nil
}

func (e *InfaiAgentEngine) registerNewParentAgent() (*loop.Agent, error) {
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

func (e *InfaiAgentEngine) Shutdown(ctx context.Context) error {
	return nil
}
