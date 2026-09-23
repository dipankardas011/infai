package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type Agent struct {
	Kind contracts.AgentKind

	mailbox        chan contracts.ChatMessage
	timelineWrites chan<- contracts.TimelineUpdate
	eventStream    chan<- contracts.EventStream
	historyUpdates chan []contracts.ChatMessage
	systemPrompt   string

	modelMu sync.RWMutex
	model   contracts.InfaiModelAdaptor

	MaxQ  uint64
	tools []contracts.Tool

	shouldAutoCompact  func(*contracts.TokenUsage) bool
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

func NewAgent(
	model contracts.InfaiModelAdaptor,
	kind contracts.AgentKind,
	activeTimeline []contracts.ChatMessage,
	timelineWrites chan<- contracts.TimelineUpdate,
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

	return &Agent{
		Kind:               kind,
		mailbox:            make(chan contracts.ChatMessage, 10),
		timelineWrites:     timelineWrites,
		eventStream:        eventStream,
		historyUpdates:     make(chan []contracts.ChatMessage, 1),
		model:              model,
		MaxQ:               o.maxQ,
		tools:              o.tools,
		shouldAutoCompact:  o.shouldCompact,
		toolCallDispatcher: toolCallDispatcher,
		systemPrompt:       systemPrompt,
		evalFunc:           o.evalFunc,
	}, nil
}

func (a *Agent) SetModel(model contracts.InfaiModelAdaptor) {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	a.model = model
}

func (a *Agent) Enqueue(ctx context.Context, message contracts.ChatMessage) error {
	select {
	case a.mailbox <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) ReplaceHistory(ctx context.Context, history []contracts.ChatMessage) error {
	history = append([]contracts.ChatMessage(nil), history...)
	select {
	case a.historyUpdates <- history:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) modelClient() contracts.InfaiModelAdaptor {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}

func (a *Agent) publishEvent(ctx context.Context, event contracts.EventStream) bool {
	select {
	case a.eventStream <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *Agent) updateState(ctx context.Context, status contracts.AgentStatus) bool {
	value := string(status)
	return a.publishEvent(ctx, contracts.EventStream{
		Kind:      contracts.NotifyAgentSessionStatus,
		Timestamp: time.Now().UTC(),
		Content:   &value,
	})
}

func (a *Agent) commitActiveTimeline(ctx context.Context, messages []contracts.ChatMessage) error {
	result := make(chan error, 1)

	select {
	case a.timelineWrites <- contracts.TimelineUpdate{Messages: append([]contracts.ChatMessage(nil), messages...), Result: result}:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Waiting for the writer in the session for any error encountered
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) drainInbox() []contracts.ChatMessage {
	var batch []contracts.ChatMessage
	for {
		select {
		case message := <-a.mailbox:
			batch = append(batch, message)
		default:
			return batch
		}
	}
}

func (a *Agent) StartLoop(ctx context.Context, activeTimeline []contracts.ChatMessage) {
	history := append([]contracts.ChatMessage(nil), activeTimeline...)

	if !a.updateState(ctx, contracts.AgentIdle) {
		return
	}

	lastHadToolCalls := false
	for iter := uint64(1); iter <= a.MaxQ; iter++ {
		unreadMessages := a.drainInbox()

		if !lastHadToolCalls && len(unreadMessages) == 0 {
			if !a.updateState(ctx, contracts.AgentIdle) {
				return
			}

			switch a.Kind {
			case contracts.SingleLoopAgent:
				_ = a.updateState(ctx, contracts.AgentCompleted)
				return
			case contracts.InteractiveAgent:
				select {
				case <-ctx.Done():
					return
				case replacement := <-a.historyUpdates: // for manual compaction
					history = replacement
					iter = 0 // back to initial as a new history is there
					continue
				case message := <-a.mailbox:
					unreadMessages = append(unreadMessages, message)
				}
			}
		}

		if !a.updateState(ctx, contracts.AgentBusy) {
			return
		}

		if len(unreadMessages) > 0 {
			if err := a.commitActiveTimeline(ctx, unreadMessages); err != nil {
				return
			}

			history = append(history, unreadMessages...)
			for _, message := range unreadMessages {
				text := message.Text()
				if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.DeltaUserPrompt, Timestamp: time.Now().UTC(), Content: &text}) {
					return
				}
			}
		}

		requestMessages := make([]contracts.ChatMessage, 0, len(history)+1)
		requestMessages = append(requestMessages, contracts.NewSystemMessage(a.systemPrompt))
		requestMessages = append(requestMessages, history...)

		reply, usage, err := a.modelClient().Generate(ctx, requestMessages, a.tools, &contracts.GenerateOptions{
			Stream: true,
			OnDelta: func(kind contracts.EventStreamKind, text string) {
				a.publishEvent(ctx, contracts.EventStream{Kind: kind, Timestamp: time.Now().UTC(), Content: &text})
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			message := err.Error()
			if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.NotifyAgentModelError, Timestamp: time.Now().UTC(), Content: &message}) {
				return
			}
			lastHadToolCalls = false
			continue
		}

		if encoded, err := json.Marshal(usage); err == nil {
			content := string(encoded)
			if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.NotifyAgentUsage, Timestamp: time.Now().UTC(), Content: &content}) {
				return
			}
		}

		messages := []contracts.ChatMessage{reply}
		lastHadToolCalls = len(reply.ToolCalls) > 0
		if lastHadToolCalls {
			for i := range reply.ToolCalls {
				call := reply.ToolCalls[i]
				if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.DeltaToolCall, Timestamp: time.Now().UTC(), ToolCall: &call}) {
					return
				}
			}
			messages = append(messages, a.toolCallDispatcher(reply.ToolCalls)...)
		} else if a.evalFunc != nil {
			result := a.evalFunc()
			messages = append(messages, contracts.NewUserMessage(fmt.Sprintf("evalFunc status: %t", result)))
		}

		if err := a.commitActiveTimeline(ctx, messages); err != nil {
			return
		}
		history = append(history, messages...)

		if a.shouldAutoCompact != nil && a.shouldAutoCompact(usage) {
			content := fmt.Sprintf("Used: %d against Total: %d", usage.TotalTokens, a.modelClient().GetModelSpecs().Model().MaxContextLength)
			if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.NotifyAgentNeedsAutoCompaction, Timestamp: time.Now().UTC(), Content: &content}) {
				return
			}

			select {
			case history = <-a.historyUpdates: // for auto compaction
				iter = 0
			case <-ctx.Done():
				return
			}
		}
	}

	a.publishEvent(ctx, contracts.EventStream{Kind: contracts.NotifyAgentReachedMaxQ, Timestamp: time.Now().UTC()})
}
