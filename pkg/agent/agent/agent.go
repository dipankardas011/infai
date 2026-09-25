package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

type Agent struct {
	Kind contracts.AgentKind

	mailbox        chan contracts.ChatMessage
	commitTimeline func(context.Context, []contracts.ChatMessage) error
	eventStream    chan<- contracts.EventStream
	workingHistory workingSessionMemory
	systemPrompt   string

	modelMu sync.RWMutex
	model   contracts.InfaiModelAdaptor

	MaxQ  uint64
	tools []contracts.Tool

	shouldAutoCompact  func(*contracts.TokenUsage) bool
	autoCompact        func(context.Context) ([]contracts.ChatMessage, error)
	userCancellation   <-chan struct{}
	toolCallDispatcher func([]contracts.ToolCall) ([]contracts.ChatMessage, bool)
	evalFunc           func() bool
}

type agentOption struct {
	maxQ          uint64
	tools         []contracts.Tool
	shouldCompact func(*contracts.TokenUsage) bool
	autoCompact   func(context.Context) ([]contracts.ChatMessage, error)
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

func WithAutoCompaction(shouldCompact func(*contracts.TokenUsage) bool, autoCompact func(context.Context) ([]contracts.ChatMessage, error)) AgentOptions {
	return func(o *agentOption) error {
		o.shouldCompact = shouldCompact
		o.autoCompact = autoCompact
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
	commitTimeline func(context.Context, []contracts.ChatMessage) error,
	eventStream chan<- contracts.EventStream,
	userCancellation <-chan struct{},
	toolCallDispatcher func([]contracts.ToolCall) ([]contracts.ChatMessage, bool),
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
		commitTimeline:     commitTimeline,
		eventStream:        eventStream,
		model:              model,
		MaxQ:               o.maxQ,
		tools:              o.tools,
		shouldAutoCompact:  o.shouldCompact,
		autoCompact:        o.autoCompact,
		userCancellation:   userCancellation,
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

func (a *Agent) MailboxEmpty() bool {
	return len(a.mailbox) == 0
}

type workingSessionMemory struct {
	mu sync.RWMutex
	h  []contracts.ChatMessage
}

func (wsm *workingSessionMemory) Set(newSessionHistory []contracts.ChatMessage) {
	wsm.mu.Lock()
	defer wsm.mu.Unlock()

	wsm.h = append([]contracts.ChatMessage(nil), newSessionHistory...)
}
func (wsm *workingSessionMemory) Get() []contracts.ChatMessage {
	wsm.mu.RLock()
	defer wsm.mu.RUnlock()

	return append([]contracts.ChatMessage(nil), wsm.h...)
}

// Append adds messages to the working history under the store's own lock. The
// loop must use this instead of Get-then-Set, otherwise it writes back a slice
// it read earlier and silently discards a replacement installed meanwhile.
func (wsm *workingSessionMemory) Append(messages ...contracts.ChatMessage) {
	wsm.mu.Lock()
	defer wsm.mu.Unlock()

	wsm.h = append(wsm.h, messages...)
}

func (a *Agent) ReplaceHistory(ctx context.Context, history []contracts.ChatMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		a.workingHistory.Set(history)
	}
	return nil
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

func (a *Agent) updateState(ctx context.Context, status contracts.SessionStatus) bool {
	value := string(status)
	return a.publishEvent(ctx, contracts.EventStream{
		Kind:      contracts.EventSessionTransitionState,
		Timestamp: time.Now().UTC(),
		Content:   &value,
	})
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
	a.workingHistory.Set(activeTimeline)

	lastHadToolCalls := false
	for iter := uint64(1); iter <= a.MaxQ; iter++ {
		unreadMessages := a.drainInbox()

		if !lastHadToolCalls && len(unreadMessages) == 0 {
			if !a.updateState(ctx, contracts.SessionIdle) {
				return
			}

			switch a.Kind {
			case contracts.SingleLoopAgent:
				_ = a.updateState(ctx, contracts.SessionCompleted)
				return
			case contracts.InteractiveAgent:
				select {
				case <-ctx.Done():
					return
				case message := <-a.mailbox:
					unreadMessages = append(unreadMessages, message)
				}
			}
		}

		if !a.updateState(ctx, contracts.SessionBusy) {
			return
		}

		if len(unreadMessages) > 0 {
			// The echo must be published before the commit that makes these
			// messages durable: a client joining between the two is given the
			// committed history plus the session's in-flight log, so an echo
			// sent after its own commit would reach that client twice.
			for _, message := range unreadMessages {
				text := message.Text()
				if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventMessageFromAgentInbox, Timestamp: time.Now().UTC(), Content: &text}) {
					return
				}
			}

			if err := a.commitTimeline(ctx, unreadMessages); err != nil {
				return
			}
			a.workingHistory.Append(unreadMessages...)

			select {
			case <-a.userCancellation:
				lastHadToolCalls = false
				continue
			default:
			}
		}

		// Read the working history only after the loop has been woken, so a
		// replacement installed while it was parked is the one that is used.
		workingSessionMem := a.workingHistory.Get()

		requestMessages := make([]contracts.ChatMessage, 0, len(workingSessionMem)+1)
		requestMessages = append(requestMessages, contracts.NewSystemMessage(a.systemPrompt))
		requestMessages = append(requestMessages, workingSessionMem...)

		reply, usage, err := a.modelClient().Generate(ctx, requestMessages, a.tools, &contracts.GenerateOptions{
			Stream: true,
			OnDelta: func(kind contracts.EventStreamKind, text string) (isCanceled bool) {
				select {
				case <-a.userCancellation:
					return true
				case a.eventStream <- contracts.EventStream{Kind: kind, Timestamp: time.Now().UTC(), Content: &text}:
					return false
				case <-ctx.Done():
					return false
				}
			},
		})
		if err != nil {
			if errors.Is(err, harnessErr.ErrTurnCanceled) {
				lastHadToolCalls = false
				continue
			}
			if ctx.Err() != nil {
				return
			}
			message := err.Error()
			if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventProviderEvent, Timestamp: time.Now().UTC(), Content: &message}) {
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
				if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventToolCall, Timestamp: time.Now().UTC(), ToolCall: &call}) { // for showing up that tool call is getting called.
					return
				}
			}
			toolMessages, dispatcherCanceled := a.toolCallDispatcher(reply.ToolCalls)
			if dispatcherCanceled {
				lastHadToolCalls = false
				continue
			}
			messages = append(messages, toolMessages...)
		} else if a.evalFunc != nil {
			result := a.evalFunc()
			messages = append(messages, contracts.NewUserMessage(fmt.Sprintf("evalFunc status: %t", result)))
		}

		if err := a.commitTimeline(ctx, messages); err != nil {
			return
		}
		a.workingHistory.Append(messages...)

		if a.shouldAutoCompact != nil && a.shouldAutoCompact(usage) {
			content := fmt.Sprintf("Used: %d against Total: %d", usage.TotalTokens, a.modelClient().GetModelSpecs().Model().MaxContextLength)
			if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventAutoCompactionTriggered, Timestamp: time.Now().UTC(), Content: &content}) {
				return
			}

			replacement, err := a.autoCompact(ctx)
			if err != nil {
				return
			}

			// Continue the next iteration from the compacted history.
			a.workingHistory.Set(replacement)
			iter = 0
		}
	}

	a.updateState(ctx, contracts.SessionMaxIterationExhausted)
}
