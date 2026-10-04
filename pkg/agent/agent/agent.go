package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

type Agent struct {
	Kind contracts.AgentKind

	mailbox        *contracts.AgentMailbox
	commitTimeline func(context.Context, []contracts.ChatMessage) error
	eventStream    chan<- contracts.EventStream
	workingHistory workingSessionMemory
	systemPrompt   string

	modelMu sync.RWMutex
	model   contracts.InfaiModelAdaptor

	MaxQ           uint64
	availableTools []contracts.Tool

	shouldAutoCompact  func(*contracts.TokenUsage) bool
	autoCompact        func(context.Context) ([]contracts.ChatMessage, error)
	userCancellation   <-chan struct{}
	toolCallDispatcher func([]contracts.ToolCall) ([]contracts.ChatMessage, bool)
	evalFunc           func(context.Context) error
}

type agentOption struct {
	maxQ          uint64
	tools         []contracts.Tool
	shouldCompact func(*contracts.TokenUsage) bool
	autoCompact   func(context.Context) ([]contracts.ChatMessage, error)
	evalFunc      func(context.Context) error
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

// Use this only for agents which are noninteractive for now use for sidecar_loop.
func WithEval(checkFunc func(context.Context) error) AgentOptions {
	return func(ao *agentOption) error {
		ao.evalFunc = checkFunc
		return nil
	}
}

func NewAgent(
	model contracts.InfaiModelAdaptor,
	kind contracts.AgentKind,
	agentMailbox *contracts.AgentMailbox,
	commitTimeline func(context.Context, []contracts.ChatMessage) error,
	eventStream chan<- contracts.EventStream,
	userCancellation <-chan struct{},
	toolCallDispatcher func([]contracts.ToolCall) ([]contracts.ChatMessage, bool),
	systemPrompt string,
	opts ...AgentOptions,
) (*Agent, error) {
	o := &agentOption{}
	for _, opt := range opts {
		if err := opt(o); err != nil {
			return nil, err
		}
	}

	return &Agent{
		Kind:               kind,
		mailbox:            agentMailbox,
		commitTimeline:     commitTimeline,
		eventStream:        eventStream,
		model:              model,
		MaxQ:               o.maxQ,
		availableTools:     o.tools,
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

func (a *Agent) StartLoop(ctx context.Context, activeTimeline []contracts.ChatMessage) {
	a.workingHistory.Set(activeTimeline)

	lastHadToolCalls := false
	lastEvalResPass := false
	if a.evalFunc == nil {
		lastEvalResPass = true // Short-circuit
	}

	for iter := uint64(1); iter <= a.MaxQ; iter++ {
		unreadMessages := a.mailbox.ConsumeAllFromInbox(ctx)

		if !lastHadToolCalls && len(unreadMessages) == 0 && lastEvalResPass {
			if !a.updateState(ctx, contracts.SessionIdle) {
				return
			}

			switch a.Kind {
			case contracts.SidecarLoopAgent, contracts.SingleLoopAgent:
				_ = a.updateState(ctx, contracts.SessionCompleted)
				return
			case contracts.InteractiveAgent:
				select {
				case <-ctx.Done():
					return
				case message := <-a.mailbox.ListenForMessageInInbox(ctx):
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
				echo := contracts.EventStream{Kind: contracts.EventMessageFromAgentInbox, Timestamp: time.Now().UTC(), Content: &text}
				if images := len(message.Images); images > 0 {
					echo.Attachments = &contracts.EventAttachments{ImageCount: images}
				}
				if !a.publishEvent(ctx, echo) {
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

		generateCtx, cancelGenerate := context.WithCancelCause(ctx)
		go func() {
			select {
			case <-a.userCancellation:
				cancelGenerate(harnessErr.ErrTurnCanceled)
			case <-generateCtx.Done():
			}
		}()

		reply, usage, err := a.modelClient().Generate(generateCtx, requestMessages, a.availableTools, &contracts.GenerateOptions{
			Stream: true,
			OnDelta: func(kind contracts.EventStreamKind, text string) (isCanceled bool) {
				return !a.publishEvent(generateCtx, contracts.EventStream{Kind: kind, Timestamp: time.Now().UTC(), Content: &text})
			},
		})
		cancelGenerate(nil)
		if err != nil {
			if errors.Is(context.Cause(generateCtx), harnessErr.ErrTurnCanceled) {
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
		if len(a.availableTools) != 0 && lastHadToolCalls {
			for i := range reply.ToolCalls {
				call := reply.ToolCalls[i]
				if !a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventToolCall, Timestamp: time.Now().UTC(), ToolCall: &call}) { // for showing up that tool call is getting called.
					return
				}
			}
			toolMessages, dispatcherCanceled := a.toolCallDispatcher(reply.ToolCalls)
			messages = append(messages, toolMessages...)
			if dispatcherCanceled {
				if err := a.commitTimeline(ctx, messages); err != nil {
					return
				}
				a.workingHistory.Append(messages...)
				lastHadToolCalls = false
				continue
			}
		} else if a.evalFunc != nil {
			evalCtx, cancelEval := context.WithCancelCause(ctx)
			cancellationDone := make(chan struct{})
			go func() {
				defer close(cancellationDone)
				select {
				case <-a.userCancellation:
					cancelEval(harnessErr.ErrTurnCanceled)
				case <-evalCtx.Done():
				}
			}()

			err := a.evalFunc(evalCtx)
			cause := context.Cause(evalCtx)
			cancelEval(nil)
			<-cancellationDone

			if errors.Is(cause, harnessErr.ErrTurnCanceled) {
				lastHadToolCalls = false
				continue
			}
			if err != nil {
				messages = append(messages, contracts.NewUserMessage(fmt.Sprintf("evaluation status: FAIL => %v", err.Error())))
				lastEvalResPass = false
			} else {
				lastEvalResPass = true
			}
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
				if a.Kind == contracts.SidecarLoopAgent || a.Kind == contracts.SingleLoopAgent {
					reason := fmt.Sprintf("sidecar compaction failed: %v", err)
					a.publishEvent(ctx, contracts.EventStream{Kind: contracts.EventSessionFatal, Timestamp: time.Now().UTC(), Content: &reason})
					return
				}
				// The history is unchanged, so there is nothing new to send to
				// the model with. Clearing the pending-work flag parks the loop
				// at the next iteration boundary instead of spending a request
				// on a context already known not to fit. A message that is
				// already queued still forces that request, which is the
				// user's call to make.
				lastHadToolCalls = false
				continue
			}

			// Continue the next iteration from the compacted history.
			a.workingHistory.Set(replacement)
			iter = 0
		}
	}

	a.updateState(ctx, contracts.SessionMaxIterationExhausted)
}
