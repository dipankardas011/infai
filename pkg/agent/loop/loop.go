package loop

import (
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type AgentStatus string

const (
	Idle    AgentStatus = "Idle"
	Working AgentStatus = "Working"
	Error   AgentStatus = "Error"
)

type Agent struct {
	Id uuid.UUID

	model    contracts.InfaiModelAdaptor
	chatCtx  contracts.SessionMemory
	Status   AgentStatus
	MaxTurns int
}

type agentOption struct {
	maxTurns int
}
type AgentOptions func(*agentOption) error

func WithMaxTurns(maxTurns int) AgentOptions {
	return func(o *agentOption) error {
		if maxTurns < 0 {
			return fmt.Errorf("maxTurns must be greater than 0")
		}
		o.maxTurns = maxTurns
		return nil
	}
}

// each agent will have a independent context(short term or running memory)
func NewAgent(id uuid.UUID, opts ...AgentOptions) (*Agent, error) {
	o := &agentOption{
		maxTurns: 65536,
	}
	for _, opt := range opts {
		if err := opt(o); err != nil {
			return nil, err
		}
	}

	return &Agent{
		Id:       id,
		model:    nil,
		chatCtx:  nil, // here we need it.
		Status:   Idle,
		MaxTurns: o.maxTurns,
	}, nil
}

func (a *Agent) SetModel(model contracts.InfaiModelAdaptor) {
	a.model = model
}

func (a *Agent) Invoke() {
	for turns := 0; turns < a.MaxTurns; turns++ {
		fmt.Println("turns:", turns)
	}
}
