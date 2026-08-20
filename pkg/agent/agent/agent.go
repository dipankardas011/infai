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

// Invoke runs the turn loop over the given conversation history, appending
// each reply. It checks ctx each turn so a canceled session context unwinds
// cleanly (children get derived contexts, so cancellation propagates to the
// whole agent tree without explicit close messages).
func (a *Agent) Invoke(ctx context.Context, history []contracts.ChatMessage) (TurnResult, error) {
	if a.model == nil {
		return TurnResult{}, ErrNoModel
	}

	a.Status = Working
	defer func() { a.Status = Idle }()

	messages := make([]contracts.ChatMessage, len(history))
	copy(messages, history)

	for turn := 0; turn < a.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return TurnResult{Status: TurnCanceled, Messages: messages}, nil
		}

		reply, err := a.model.Generate(ctx, messages, nil)
		if err != nil {
			if ctx.Err() != nil {
				// Canceled mid-call: report as canceled, not a model error.
				return TurnResult{Status: TurnCanceled, Messages: messages}, nil
			}
			a.Status = Error
			return TurnResult{Status: TurnDone, Messages: messages}, fmt.Errorf("agent: turn %d: %w", turn, err)
		}
		messages = append(messages, reply)

		if a.turnHook != nil {
			a.turnHook(reply)
		}

		break
	}

	// Canceled on the final iteration's boundary — report it, not "done".
	if err := ctx.Err(); err != nil {
		return TurnResult{Status: TurnCanceled, Messages: messages}, nil
	}

	return TurnResult{Status: TurnDone, Messages: messages}, nil
}
