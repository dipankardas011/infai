package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

type AgentStatus string

const (
	Idle            AgentStatus = "idle"
	Working         AgentStatus = "working"
	PendingApproval AgentStatus = "pending_approval"
	Error           AgentStatus = "error"
)

var ErrNoModel = errors.New("agent: no model adapter set")

type Agent struct {
	Id uuid.UUID

	model    contracts.InfaiModelAdaptor
	chatCtx  contracts.SessionMemory
	turnHook func(contracts.ChatMessage)
	Status   AgentStatus
	MaxTurns int
}

type agentOption struct {
	maxTurns int
	turnHook func(contracts.ChatMessage)
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

func WithTurnHook(hook func(contracts.ChatMessage)) AgentOptions {
	return func(o *agentOption) error {
		o.turnHook = hook
		return nil
	}
}

// NewAgent creates an agent with an independent short-term context (the
// message history built up by Invoke).
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
		chatCtx:  nil,
		turnHook: o.turnHook,
		Status:   Idle,
		MaxTurns: o.maxTurns,
	}, nil
}

func (a *Agent) SetModel(model contracts.InfaiModelAdaptor) {
	a.model = model
}

// Invoke is the primitive chatbot: seed the conversation with a system and
// user prompt, ask the model once, append the reply to the history and emit
// it through the turn hook. Multi-turn continuation plugs into this loop.
func (a *Agent) Invoke(ctx context.Context, systemPrompt, userPrompt string) ([]contracts.ChatMessage, error) {
	if a.model == nil {
		return nil, ErrNoModel
	}

	a.Status = Working
	defer func() { a.Status = Idle }()

	messages := []contracts.ChatMessage{
		contracts.NewSystemMessage(systemPrompt),
		contracts.NewUserMessage(userPrompt),
	}

	for turn := 0; turn < a.MaxTurns; turn++ {
		reply, err := a.model.Generate(ctx, messages, nil)
		if err != nil {
			a.Status = Error
			return messages, fmt.Errorf("agent: turn %d: %w", turn, err)
		}
		messages = append(messages, reply)

		if a.turnHook != nil {
			a.turnHook(reply)
		}

		break
	}

	return messages, nil
}
