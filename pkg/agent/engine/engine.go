// This is the main handling procecss or manager so you can say
package engine

import (
	"context"
	"log/slog"

	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

type AgentCommunicateEngine struct {
	// TODO: we need to define the contract
	R <-chan struct{}
	W chan<- struct{}
}

type InfaiAgentEngine struct {
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig

	// Need to handle N number of agents as go routine so for that case a better place ig is goroutune manager or a workerPool manager like with one agent only when launch and when needed we can call and when there is a specific <if_cond{subagent}> then we can branch it in some sort by tell engine to register Another agent given a UUIDv7 of a agent it will check and register based on resource constraints of the harness
	// for communication we can use a channel a RWMap which we can use for retrival of neccessary pipe for sharing info

	// WARN: we do need to properly handle the Concurrency.
	agentMapping  map[uuid.UUID]ds.Set[uuid.UUID]       // Parent -> Child
	runtime_comms map[uuid.UUID]*AgentCommunicateEngine // By this when N no of children can send comms with engine and even the
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) *InfaiAgentEngine {
	return &InfaiAgentEngine{
		bgLogger:      bgLogger,
		engineCfg:     cfg,
		agentMapping:  make(map[uuid.UUID]ds.Set[uuid.UUID]),
		runtime_comms: make(map[uuid.UUID]*AgentCommunicateEngine),
	}
}

func (e *InfaiAgentEngine) Run() error {
	return nil
}

func (e *InfaiAgentEngine) Shutdown(ctx context.Context) error {
	return nil
}
