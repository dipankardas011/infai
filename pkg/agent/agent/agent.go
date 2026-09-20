package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type Agent struct {
	Kind           contracts.AgentKind
	Status         contracts.AgentStatus
	Mailbox        chan contracts.ChatMessage
	activeTimeline []contracts.ChatMessage

	storeToTimeline chan<- []contracts.ChatMessage
	eventStream     chan<- contracts.EventStream
	systemPrompt    string

	model contracts.InfaiModelAdaptor

	MaxQ  uint64
	tools []contracts.Tool

	shouldCompact      func(*contracts.TokenUsage) bool
	toolCallDispatcher func([]contracts.ToolCall) []contracts.ChatMessage
	evalFunc           func() bool
}

type agentOption struct {
	maxQ          uint64
	tools         []contracts.Tool
	shouldCompact func(*contracts.TokenUsage) bool
	evalFunc      func() bool
}

type AgentOptions func(*agentOption) error

func WithMaxTurns(maxTurns uint64) AgentOptions {
	return func(o *agentOption) error {
		o.maxQ = maxTurns
		return nil
	}
}

func WithTools(tools ...contracts.Tool) AgentOptions {
	return func(o *agentOption) error {
		o.tools = append([]contracts.Tool(nil), tools...)
		return nil
	}
}

func WithAutoCompaction(check func(*contracts.TokenUsage) bool) AgentOptions {
	return func(o *agentOption) error {
		o.shouldCompact = check
		return nil
	}
}

func WithEval(checkFunc func() bool) AgentOptions {
	return func(ao *agentOption) error {
		ao.evalFunc = checkFunc
		return nil
	}
}

// NewAgent creates an agent with an independent short-term context (the
// message history built up by Invoke).
func NewAgent(
	sessionCtx context.Context,
	model contracts.InfaiModelAdaptor,
	kind contracts.AgentKind,
	activeTimeline []contracts.ChatMessage,
	aolTimeline chan<- []contracts.ChatMessage,
	eventStream chan<- contracts.EventStream,

	toolCallDispatcher func([]contracts.ToolCall) []contracts.ChatMessage,
	systemPrompt string,
	opts ...AgentOptions,
) (*Agent, error) {
	o := &agentOption{maxQ: math.MaxUint16}
	for _, opt := range opts {
		if err := opt(o); err != nil {
			return nil, err
		}
	}

	a := &Agent{
		Kind:               kind,
		Mailbox:            make(chan contracts.ChatMessage, 10),
		storeToTimeline:    aolTimeline,
		toolCallDispatcher: toolCallDispatcher,
		eventStream:        eventStream,

		model: model,
		MaxQ:  o.maxQ,
		tools: o.tools,

		shouldCompact: o.shouldCompact,
		systemPrompt:  systemPrompt,
		evalFunc:      o.evalFunc,
	}
	a.SetActiveTimelineHistory(activeTimeline)

	go a.StartLoop(sessionCtx)
	return a, nil
}

func (a *Agent) UpdateState(status contracts.AgentStatus) {
	a.Status = status
	a.eventStream <- contracts.NewEventStream(contracts.NotifyAgentSessionStatus, string(status))
}

func (a *Agent) SetModel(model contracts.InfaiModelAdaptor) { a.model = model }

func (a *Agent) drainInbox() []contracts.ChatMessage {
	var batch []contracts.ChatMessage
	for {
		select {
		case tm := <-a.Mailbox:
			batch = append(batch, tm)
		default:
			return batch
		}
	}
}

func (a *Agent) subscribeToInbox() <-chan contracts.ChatMessage { return a.Mailbox }

// Use it when you compacted and want the agent to use a new history simple.
func (a *Agent) SetActiveTimelineHistory(his []contracts.ChatMessage) {
	a.activeTimeline = append([]contracts.ChatMessage(nil), his...)
}

func (a *Agent) StartLoop(sessCtx context.Context) {
	tmpAgentEvents := make(chan contracts.EventStream, 1000)
	a.UpdateState(contracts.AgentIdle)

	// Fan-out ticker: drains tmpAgentEvents into EventStream at a steady rate,
	// forwards actual items (not the channel handle), and exits cleanly on
	// session cancellation instead of leaking.
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		drainOnce := func() bool { // returns false if session died mid-drain
			for {
				select {
				case ev := <-tmpAgentEvents:
					select {
					case a.eventStream <- ev:
					case <-sessCtx.Done():
						return false
					}
				default:
					return true // buffer empty, done for this tick
				}
			}
		}

		for {
			select {
			case <-sessCtx.Done():
				return
			case <-ticker.C:
				if !drainOnce() {
					return
				}
			}
		}
	}()

	handleStreamsFromModel := func(deltaKind contracts.EventStreamKind, text string) {
		select {
		case tmpAgentEvents <- contracts.NewEventStream(deltaKind, text):
		default:
			// event buffer full — drop rather than stall generation;
			// deltas are reconstructible from AOLStore, safe to lose
		}
	}
	lastHadToolCalls := false

	for range a.MaxQ {

		unreadMessages := a.drainInbox()

		isIdle := !lastHadToolCalls && len(unreadMessages) == 0

		if isIdle {
			a.UpdateState(contracts.AgentIdle)
			switch a.Kind {
			case contracts.SingleLoopAgent:
				select {
				case <-sessCtx.Done():
					a.UpdateState(contracts.AgentClosed)
				default:
					a.UpdateState(contracts.AgentCompleted)
				}
				return

			case contracts.InteractiveAgent:
				select {
				case <-sessCtx.Done():
					a.UpdateState(contracts.AgentClosed)
					return
				case msg := <-a.subscribeToInbox():
					unreadMessages = append(unreadMessages, msg)
				}
			}
		}

		// we try to catch it if the history is absent.
		if a.activeTimeline == nil {
			start := time.Now().UTC()
			tc := time.NewTicker(time.Minute)

			for a.activeTimeline == nil {
				select {
				case <-sessCtx.Done():
					a.UpdateState(contracts.AgentClosed)
					return
				case curr := <-tc.C:
					a.eventStream <- contracts.NewEventStream(
						contracts.NotifyAgentMissingHistory,
						fmt.Sprintf("waiting for the activeTimeline to be available. Since: %s", curr.Sub(start).Round(time.Second).String()),
					)
				}
			}
		}

		a.UpdateState(contracts.AgentBusy)

		requestMessages := make([]contracts.ChatMessage, 0, len(a.activeTimeline)+len(unreadMessages))

		switch hasAddedInboxAsTurnRequest := len(unreadMessages) > 0; hasAddedInboxAsTurnRequest {
		case true:
			a.storeToTimeline <- unreadMessages
			a.activeTimeline = append(a.activeTimeline, unreadMessages...)
			for _, v := range unreadMessages {
				a.eventStream <- contracts.NewEventStream(
					contracts.DeltaUserPrompt,
					v.Text(), // for the image based ones the text placeholder is there na? then we don't need anything
				)
			}
		case false:
		}

		select {
		case <-sessCtx.Done():
			a.UpdateState(contracts.AgentClosed)
			return
		default:
			requestMessages = append(requestMessages, contracts.NewSystemMessage(a.systemPrompt))
			requestMessages = append(requestMessages, a.activeTimeline...)
		}

		reply, u, err := a.model.Generate(sessCtx, requestMessages, a.tools, &contracts.GenerateOptions{
			Stream:  true,
			OnDelta: handleStreamsFromModel,
		})
		if err != nil {
			if sessCtx.Err() != nil {
				a.UpdateState(contracts.AgentClosed)
				return
			}

			a.eventStream <- contracts.NewEventStream(contracts.NotifyAgentModelError, err.Error())

			continue // to return back to the next iteration
		}

		if _u, err := json.Marshal(u); err == nil {
			a.eventStream <- contracts.NewEventStream(contracts.NotifyAgentUsage, string(_u))
		}

		if len(reply.ToolCalls) == 0 {
			a.storeToTimeline <- []contracts.ChatMessage{reply}
			a.activeTimeline = append(a.activeTimeline, reply)

			if a.evalFunc != nil { // TODO: when the eval arrives
				res := a.evalFunc()
				a.activeTimeline = append(a.activeTimeline, contracts.ChatMessage{
					Role:    "assistant",
					Content: new(fmt.Sprintf("evalFunc status: %t", res)),
				})
			}
		} else {
			for _, toolCall := range reply.ToolCalls {
				a.eventStream <- contracts.NewEventStream(
					contracts.DeltaToolCall,
					contracts.ToolCallDisplay(toolCall),
				)
			}
			x := append(
				[]contracts.ChatMessage{reply},
				a.toolCallDispatcher(reply.ToolCalls)...,
			)
			a.storeToTimeline <- x
			a.activeTimeline = append(a.activeTimeline, x...)
		}

		if a.shouldCompact != nil && a.shouldCompact(u) {
			a.activeTimeline = nil // explicitly made it zero.

			a.eventStream <- contracts.NewEventStream(
				contracts.NotifyAgentNeedsAutoCompaction,
				fmt.Sprintf("Used: %d against Total: %d",
					u.TotalTokens,
					a.model.GetModelSpecs().Model().MaxContextLength,
				),
			)
		}
	}
}
