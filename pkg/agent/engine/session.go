package engine

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/actuators"
	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/ds"
	"github.com/google/uuid"
)

var (
	ErrSessionClosed  = errors.New("session closed")
	ErrNoProvider     = errors.New("engine: no provider configured")
	errApprovalDenied = errors.New("tool execution was denied by the user")
)

// InfaiAgentSession is the persistent state of one conversation. It is a
// passive object: it holds history and the agent tree, and Chat() runs the
// loop against it. Between Chat calls the session is idle — no goroutine is
// held — so it stays registered until explicitly closed. Every turn is
// streamed through the session's event hub, which fans out to the live user
// sink (SSE/stdout); durable history is written directly to the timeline.
type InfaiAgentSession struct {
	sessionID uuid.UUID

	l *slog.Logger

	model contracts.InfaiModelAdaptor

	mu      sync.Mutex
	closed  bool
	history []contracts.ChatMessage

	meta                store.SessionMeta
	timeline            *store.Timeline
	store               *store.SessionStore
	events              *store.SessionEventHub
	persisted           int
	pendingBranchParent uuid.UUID

	auditorPolicy  *auditor.AuditorPolicy
	availableTools []contracts.Tool

	// WARN: we do need to properly handle the Concurrency.
	agentMapping    map[uuid.UUID]*ds.Set[uuid.UUID] // Parent -> Child
	agentComms      *comms.AgentComms
	approvalMu      sync.Mutex
	pendingApproval *pendingApproval

	sessionAgentId uuid.UUID

	Agents map[uuid.UUID]*agent.Agent
}

type pendingApproval struct {
	request  ApprovalRequest
	decision chan ApprovalDecisionFromClient
}

// NewSession creates a fresh session bound to the given provider and model.
func NewSession(l *slog.Logger, p *store.Provider, model string, ctxWindow int, cwd string, ss *store.SessionStore) (*InfaiAgentSession, error) {
	if p == nil {
		return nil, ErrNoProvider
	}

	o := &InfaiAgentSession{
		l:             l,
		model:         models.NewOpenAICompatableAPI(p.Endpoint, model, p.APIKey),
		store:         ss,
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		agentComms:    comms.NewAgentComms(),
		Agents:        make(map[uuid.UUID]*agent.Agent),
		auditorPolicy: auditor.NewAuditorPolicy(),
	}

	if v, err := uuid.NewV7(); err != nil {
		return nil, err
	} else {
		o.sessionID = v
	}

	now := time.Now().UTC()
	o.meta = store.SessionMeta{
		ID:            o.sessionID,
		Provider:      p.Name,
		Model:         model,
		Cwd:           cwd,
		ContextWindow: ctxWindow,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	o.events = store.NewSessionEventHub()
	if err := ss.SaveMeta(o.meta); err != nil {
		return nil, err
	}

	timeline, err := ss.LoadSessionTimelineClient(o.meta.ID)
	if err != nil {
		return nil, err
	}
	o.timeline = timeline

	o.auditorPolicy.SetPolicy(contracts.ReadTool, auditor.HumanPolicy)
	o.availableTools = append([]contracts.Tool{}, actuators.ReadTool())

	systemPrompt, err := GetBasicSystemPrompt(o.availableTools, nil)
	if err != nil {
		return nil, err
	}
	firstAgent, err := o.registerNewParentAgent(systemPrompt)
	if err != nil {
		return nil, err
	}
	o.sessionAgentId = firstAgent.Id

	return o, nil
}

// NewResumedSession rebuilds a session from the active timeline ancestry. The
// caller resolves lazy blob records before constructing the chat history.
func NewResumedSession(l *slog.Logger, p *store.Provider, meta store.SessionMeta, history []contracts.ChatMessage, timeline *store.Timeline, sessionStore *store.SessionStore) (*InfaiAgentSession, error) {
	if p == nil {
		return nil, ErrNoProvider
	}

	o := &InfaiAgentSession{
		l:             l,
		model:         models.NewOpenAICompatableAPI(p.Endpoint, meta.Model, p.APIKey),
		agentMapping:  make(map[uuid.UUID]*ds.Set[uuid.UUID]),
		agentComms:    comms.NewAgentComms(),
		Agents:        make(map[uuid.UUID]*agent.Agent),
		sessionID:     meta.ID,
		meta:          meta,
		store:         sessionStore,
		history:       history,
		persisted:     len(history),
		timeline:      timeline,
		events:        store.NewSessionEventHub(),
		auditorPolicy: auditor.NewAuditorPolicy(),
	}

	if err := sessionStore.SaveMeta(meta); err != nil {
		return nil, err
	}

	o.auditorPolicy.SetPolicy(contracts.ReadTool, auditor.HumanPolicy)
	o.availableTools = append([]contracts.Tool{}, actuators.ReadTool())
	systemPrompt, err := GetBasicSystemPrompt(o.availableTools, nil)
	if err != nil {
		return nil, err
	}

	firstAgent, err := o.registerNewParentAgent(systemPrompt)
	if err != nil {
		return nil, err
	}
	o.sessionAgentId = firstAgent.Id

	return o, nil
}

func (s *InfaiAgentSession) ID() uuid.UUID {
	return s.sessionID
}

func (s *InfaiAgentSession) Meta() store.SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// EventHub exposes the session's live broadcaster so the server can attach the
// live SSE sink per request.
func (s *InfaiAgentSession) EventHub() *store.SessionEventHub {
	return s.events
}

// Timeline returns every event in the session graph. Blob-backed records remain
// placeholders until explicitly resolved.
func (s *InfaiAgentSession) Timeline() ([]store.Event, uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.timeline.LoadEntireTimeline()
	return events, s.timeline.CurrentHeadEventID(), err
}

func (s *InfaiAgentSession) SelectBranch(eventID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.timeline.LoadEvent(eventID); err != nil {
		return err
	}
	s.pendingBranchParent = eventID
	return nil
}

// SetModel rebuilds the session's model adapter for the given provider and
// model and records the change in the session meta.
func (s *InfaiAgentSession) SetModel(p *store.Provider, name string, ctxWindow int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setModelLocked(p, name, ctxWindow)
}

func (s *InfaiAgentSession) setModelLocked(p *store.Provider, name string, ctxWindow int) {
	if p == nil {
		return
	}
	s.model = models.NewOpenAICompatableAPI(p.Endpoint, name, p.APIKey)
	if a := s.Agents[s.sessionAgentId]; a != nil {
		a.SetModel(s.model)
	}
	s.meta.Provider = p.Name
	s.meta.Model = name
	s.meta.ContextWindow = ctxWindow
	s.meta.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveMeta(s.meta); err != nil {
		s.l.Error("persist session metadata", "session_id", s.sessionID, "error", err)
	}
}

// CompactChat generates and persists a continuation summary, advances the
// timeline HEAD with a compaction event, and resets active history.
func (s *InfaiAgentSession) CompactChat(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}
	s.l.Info("manual compaction requested", "history_messages", len(s.history))

	systemPrompt, history, err := compactionInput(s.history)
	if err != nil {
		s.l.Error("manual compaction input failed", "error", err)
		return err
	}
	ctx = actuators.WithWorkspace(ctx, s.meta.Cwd)
	if err := s.compactChat(ctx, systemPrompt, history); err != nil {
		s.l.Error("manual compaction failed", "error", err)
		return err
	}
	s.meta.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveMeta(s.meta); err != nil {
		s.l.Error("persist session metadata", "session_id", s.sessionID, "error", err)
	}
	return nil
}

// Chat runs the base agent loop for one user prompt against the session's
// persistent history and returns the outcome. The session stays registered
// and idle after the call; the next Chat reuses the same conversation. New
// messages, usage and meta are persisted through the recorder as they settle.
func (s *InfaiAgentSession) Chat(ctx context.Context, prompt string, opts ChatOptions) (*ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrSessionClosed
	}
	parentID := s.timeline.CurrentHeadEventID()
	switch {
	case s.pendingBranchParent != uuid.Nil:
		events, err := s.timeline.LoadActiveContextAt(s.pendingBranchParent)
		if err != nil {
			return nil, err
		}
		history, err := timelineHistory(s.timeline, events)
		if err != nil {
			return nil, err
		}
		s.history = history
		s.persisted = len(history)
		parentID = s.pendingBranchParent
		s.pendingBranchParent = uuid.Nil
	}

	s.history = append(s.history, contracts.NewUserMessage(prompt))

	// Persist the user's message before generation so a hard crash can never
	// lose what was typed. The reply is synced at turn end; an interrupted
	// reply is the only thing a crash can take.
	msg := s.history[len(s.history)-1]

	var appendErr error
	if parentID == s.timeline.CurrentHeadEventID() {
		_, appendErr = s.timeline.AppendToHead(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &msg})
	} else {
		_, appendErr = s.timeline.BranchFromEventID(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &msg}, parentID)
	}
	if appendErr != nil {
		s.l.Error("persist user message", "session_id", s.sessionID, "error", appendErr)
		return nil, fmt.Errorf("persist user message: %w", appendErr)
	}

	s.persisted = len(s.history)

	agentLoop := s.Agents[s.sessionAgentId]
	if agentLoop == nil {
		return nil, errors.New("session: base agent missing")
	}
	agentLoop.SetDeltaHook(func(kind contracts.DeltaKind, text string) {
		s.events.Publish(store.Record{Kind: store.KindDelta, Timestamp: time.Now().UTC(), DeltaKind: kind, Text: text})
	})

	result, err := s.runAgent(ctx, agentLoop)
	toolCalls := 0
	for _, message := range result.Messages {
		toolCalls += len(message.ToolCalls)
	}
	s.l.DebugContext(ctx, "agent invocation completed",
		"status", result.Status.String(),
		"messages", len(result.Messages),
		"tool_calls", toolCalls,
		"usage", result.Usage != nil,
	)
	if err != nil {
		return nil, err
	}
	compacted := false
	if result.Status == agent.TurnNeedsCompaction {
		s.l.InfoContext(ctx, "automatic compaction requested", "session_id", s.sessionID)
		if result.Usage != nil {
			s.l.Warn("automatic compaction triggered",
				"prompt_tokens", result.Usage.PromptTokens,
				"completion_tokens", result.Usage.CompletionTokens,
				"total_tokens", result.Usage.TotalTokens,
				"context_window", s.meta.ContextWindow,
				"threshold_percent", 80,
			)
		}
		systemPrompt, history, err := compactionInput(s.history)
		if err != nil {
			s.l.Error("automatic compaction input failed", "error", err)
			return nil, err
		}
		if err := s.compactChat(ctx, systemPrompt, history); err != nil {
			s.l.Error("automatic compaction failed", "error", err)
			return nil, err
		}
		compacted = true
		result, err = s.runAgent(ctx, agentLoop)
		if err != nil {
			return nil, err
		}
		if result.Status == agent.TurnNeedsCompaction {
			s.l.Warn("automatic compaction requested again; stopping after one continuation", "session_id", s.sessionID)
		}
	}
	s.meta.TurnCount++
	s.meta.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveMeta(s.meta); err != nil {
		s.l.Error("persist session metadata", "session_id", s.sessionID, "error", err)
	}

	contextTokens := 0
	if !compacted && result.Usage != nil {
		contextTokens = result.Usage.TotalTokens
		if contextTokens <= 0 {
			contextTokens = result.Usage.PromptTokens + result.Usage.CompletionTokens
		}
	}
	return &ChatResult{
		SessionID:        s.sessionID,
		Status:           result.Status,
		Reply:            lastAssistantText(result.Messages),
		ReasoningContent: lastAssistantReasoning(result.Messages),
		Pending:          nil, // populated by the session approval coordinator
		Usage:            result.Usage,
		ContextTokens:    contextTokens,
	}, nil
}

// runAgent owns one complete agent/comms invocation and persists the messages
// it produced before returning control to Chat.
func (s *InfaiAgentSession) runAgent(ctx context.Context, agentLoop *agent.Agent) (agent.TurnResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = actuators.WithWorkspace(runCtx, s.meta.Cwd)
	defer cancel()

	commErr := make(chan error, 1)
	go func() {
		commErr <- s.handleAgentComms(runCtx)
	}()

	agentErr := make(chan error, 1)
	var result agent.TurnResult
	var err error
	go func() {
		result, err = agentLoop.Invoke(runCtx, s.history)
		agentErr <- err
	}()

	var commRunErr error
	select {
	case err = <-agentErr:
		cancel()
		commRunErr = <-commErr
	case commRunErr = <-commErr:
		cancel()
		err = <-agentErr
	}
	if err == nil && commRunErr != nil && !errors.Is(commRunErr, context.Canceled) {
		err = commRunErr
	}

	if result.Messages != nil {
		s.history = result.Messages
		if persistErr := s.persistMessagesLocked(); persistErr != nil {
			return result, persistErr
		}
	}
	return result, err
}

// persistMessagesLocked writes history entries beyond the watermark as message
// records, deriving tool-call and tool-result records when present. Caller
// holds s.mu.
func (s *InfaiAgentSession) persistMessagesLocked() error {
	for i := s.persisted; i < len(s.history); i++ {
		m := s.history[i]
		if _, err := s.timeline.AppendToHead(store.Record{Kind: store.KindMessage, Timestamp: time.Now().UTC(), Message: &m}); err != nil {
			s.l.Error("persist message", "session_id", s.sessionID, "error", err)
			return fmt.Errorf("persist message: %w", err)
		}
	}
	s.persisted = len(s.history)
	return nil
}

func (s *InfaiAgentSession) resetHistoryLocked(summary string) {
	s.history = []contracts.ChatMessage{contracts.NewUserMessage("<context-summary>\n" + summary + "\n</context-summary>")}
	s.persisted = len(s.history)
	s.l.Info("session context compacted", "session_id", s.sessionID, "context_window", s.meta.ContextWindow)
}

// Compact is kept as an alias for CompactChat.
func (s *InfaiAgentSession) Compact(ctx context.Context) error {
	return s.CompactChat(ctx)
}

func (s *InfaiAgentSession) shouldCompact(usage *contracts.TokenUsage) bool {
	if usage == nil || s.meta.ContextWindow <= 0 {
		s.l.Debug("automatic compaction not evaluated",
			"has_usage", usage != nil,
			"context_window", s.meta.ContextWindow,
		)
		return false
	}
	used := usage.TotalTokens
	if used <= 0 {
		used = usage.PromptTokens + usage.CompletionTokens
	}
	threshold := s.meta.ContextWindow * 80 / 100
	shouldCompact := used >= threshold
	s.l.Debug("automatic compaction evaluated",
		"used_tokens", used,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"total_tokens", usage.TotalTokens,
		"context_window", s.meta.ContextWindow,
		"threshold_tokens", threshold,
		"threshold_percent", 80,
		"should_compact", shouldCompact,
	)
	return shouldCompact
}

// TODO: we do need to revisit to optimize the summary like storing a flattened history
func compactionInput(history []contracts.ChatMessage) (string, []contracts.ChatMessage, error) {
	systemPrompt, err := CompactionAgentSystemPrompt()
	if err != nil {
		return "", nil, err
	}
	input := append([]contracts.ChatMessage(nil), history...)
	input = append(input, contracts.NewUserMessage("Can you compact based on the history?"))
	return systemPrompt, input, nil
}

func (s *InfaiAgentSession) compactChat(ctx context.Context, systemPrompt string, history []contracts.ChatMessage) error {
	s.l.Debug("compaction started", "input_messages", len(history))
	s.events.Publish(store.Record{Kind: store.KindDelta, Timestamp: time.Now().UTC(), DeltaKind: contracts.DeltaStatus, Text: "compacting"})
	result, err := s.summarize(ctx, systemPrompt, history)
	if err != nil {
		s.l.Error("compaction summary generation failed", "error", err)
		return err
	}
	if result.Summary == "" {
		err := errors.New("session: compaction produced an empty summary")
		s.l.Error("compaction summary is empty", "error", err)
		return err
	}
	if _, err := s.timeline.AppendToHead(store.Record{
		Kind: store.KindCompaction, Timestamp: time.Now().UTC(),
		Compaction: &store.CompactionRecord{Summary: result.Summary},
	}); err != nil {
		s.l.Error("persist compaction event failed", "error", err)
		return err
	}
	s.resetHistoryLocked(result.Summary)
	s.l.InfoContext(ctx, "compaction completed", "summary_chars", len(result.Summary))
	s.events.Publish(store.Record{Kind: store.KindDelta, Timestamp: time.Now().UTC(), DeltaKind: contracts.DeltaCompactionSummary, Text: result.Summary})
	s.events.Publish(store.Record{Kind: store.KindDelta, Timestamp: time.Now().UTC(), DeltaKind: contracts.DeltaStatus, Text: "compacted"})
	return nil
}

func (s *InfaiAgentSession) summarize(ctx context.Context, systemPrompt string, history []contracts.ChatMessage) (agent.CompactionResult, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return agent.CompactionResult{}, err
	}
	compactionAgent, err := s.registerTransientAgent(id, systemPrompt)
	if err != nil {
		return agent.CompactionResult{}, err
	}
	defer s.removeAgent(id)

	compactionAgent.SetModel(s.model)
	result, err := compactionAgent.Invoke(ctx, history)
	if err != nil {
		return agent.CompactionResult{}, err
	}
	return agent.CompactionResult{Summary: lastAssistantText(result.Messages)}, nil
}

func (s *InfaiAgentSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.events != nil {
		s.events.Close()
	}
	if s.agentComms != nil {
		s.agentComms.Close()
	}
	if s.timeline != nil {
		_ = s.timeline.Close()
	}
}

func (s *InfaiAgentSession) handleAgentComms(ctx context.Context) error {
	for {
		msg, err := s.agentComms.ReceiveFromAgents(ctx)
		if err != nil {
			return err
		}

		switch msg.Kind {
		case comms.AgentCommTool:
			if err := s.handleToolComm(ctx, msg); err != nil {
				return err
			}
		case comms.AgentCommSubagent:
			if err := s.handleSubagentComm(ctx, msg); err != nil {
				return err
			}
		}
	}
}

func (s *InfaiAgentSession) handleToolComm(ctx context.Context, msg comms.AgentComm) error {
	var calls []contracts.ToolCall
	if err := json.Unmarshal(msg.Payload, &calls); err != nil {
		return err
	}

	toolMessages := make([]contracts.ChatMessage, 0, len(calls))
	for _, call := range calls {
		policy := s.auditorPolicy.Check(contracts.ToolType(call.Function.Name))
		s.l.DebugContext(ctx, "tool call received",
			"agent_id", msg.From,
			"call_id", call.ID,
			"tool", call.Function.Name,
			"policy", policy.String(),
		)

		s.events.Publish(store.Record{
			Kind:      store.KindDelta,
			Timestamp: time.Now().UTC(),
			DeltaKind: contracts.DeltaStatus,
			Text:      fmt.Sprintf("tool: %s", call.Function.Name),
		})

		status := string(contracts.ToolExecutionSuccess)
		toolError := ""
		var content string
		var err error

		switch policy {
		case auditor.AllowPolicy:
			content, err = actuators.ExecuteToolCall(ctx, call)
		case auditor.HumanPolicy:
			content, err = s.executeAfterApproval(ctx, msg.From, call)
		default:
			status = string(contracts.ToolExecutionDenied)
			toolError = "tool execution was denied by session policy"
			content = toolError
		}

		if err != nil {
			if errors.Is(err, errApprovalDenied) {
				status = string(contracts.ToolExecutionDenied)
			} else {
				status = string(contracts.ToolExecutionError)
			}
			toolError = err.Error()
			content = err.Error()
		}

		s.l.DebugContext(ctx, "tool call completed",
			"agent_id", msg.From,
			"call_id", call.ID,
			"tool", call.Function.Name,
			"status", status,
		)

		if _, persistErr := s.timeline.AppendToHead(store.Record{
			Kind:      store.KindToolResult,
			Timestamp: time.Now().UTC(),
			ToolResult: &store.ToolResultRecord{
				CallID: call.ID,
				Status: status,
				Output: content,
				Error:  toolError,
			},
		}); persistErr != nil {
			return fmt.Errorf("persist tool result: %w", persistErr)
		}
		toolMessages = append(toolMessages, contracts.NewToolMessage(call.ID, content))
	}
	payload, err := json.Marshal(toolMessages)
	if err != nil {
		return err
	}
	return s.agentComms.SendToAgent(ctx, msg.From, comms.AgentComm{
		ID:      uuid.New(),
		ReplyTo: msg.ID,
		From:    s.sessionID,
		To:      msg.From,
		Kind:    comms.AgentCommMessage,
		Payload: payload,
	})
}

func (s *InfaiAgentSession) executeAfterApproval(ctx context.Context, agentID uuid.UUID, call contracts.ToolCall) (string, error) {
	approvalID, err := uuid.NewV7()
	if err != nil {
		return "", err
	}

	fingerprintInput := approvalID.String() + s.sessionID.String() + agentID.String() + call.ID + call.Function.Name + call.Function.Arguments
	hash := sha256.Sum256([]byte(fingerprintInput))
	fingerprint := hex.EncodeToString(hash[:])

	request := ApprovalRequest{
		ID:          approvalID,
		SessionID:   s.sessionID,
		AgentID:     agentID,
		ToolCall:    call,
		Fingerprint: fingerprint,
		CreatedAt:   time.Now().UTC(),
	}

	pending := &pendingApproval{
		request:  request,
		decision: make(chan ApprovalDecisionFromClient, 1),
	}

	s.approvalMu.Lock()
	if s.pendingApproval != nil {
		s.approvalMu.Unlock()
		return "", errors.New("another tool approval is already pending")
	}
	s.pendingApproval = pending
	s.approvalMu.Unlock()

	s.events.Publish(store.Record{
		Kind:      store.KindApprovalRequested,
		Timestamp: time.Now().UTC(),
		Approval: &store.ApprovalEvent{
			ID:          request.ID,
			SessionID:   request.SessionID,
			AgentID:     request.AgentID,
			Fingerprint: request.Fingerprint,
			ToolCall:    &request.ToolCall,
		},
	})
	s.l.InfoContext(ctx, "tool approval requested",
		"approval_id", request.ID,
		"agent_id", request.AgentID,
		"tool", request.ToolCall.Function.Name,
	)

	select {
	case decision := <-pending.decision:
		s.events.Publish(store.Record{
			Kind:      store.KindApprovalResolved,
			Timestamp: time.Now().UTC(),
			Approval: &store.ApprovalEvent{
				ID:          request.ID,
				SessionID:   request.SessionID,
				AgentID:     request.AgentID,
				Fingerprint: request.Fingerprint,
				Decision:    string(decision.Decision),
				Reason:      decision.Reason,
			},
		})
		s.l.InfoContext(ctx, "tool approval resolved",
			"approval_id", request.ID,
			"decision", decision.Decision,
		)
		if decision.Decision != ApprovalApprove {
			return "", errApprovalDenied
		}
		return actuators.ExecuteToolCall(ctx, call)
	case <-ctx.Done():
		s.approvalMu.Lock()
		if s.pendingApproval == pending {
			s.pendingApproval = nil
		}
		s.approvalMu.Unlock()
		s.events.Publish(store.Record{
			Kind:      store.KindApprovalCanceled,
			Timestamp: time.Now().UTC(),
			Approval: &store.ApprovalEvent{
				ID:          request.ID,
				SessionID:   request.SessionID,
				AgentID:     request.AgentID,
				Fingerprint: request.Fingerprint,
			},
		})
		return "", ctx.Err()
	}
}

func (s *InfaiAgentSession) ResolveApproval(id uuid.UUID, decision ApprovalDecisionFromClient) error {
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()

	if s.pendingApproval == nil || s.pendingApproval.request.ID != id {
		return errors.New("approval not found or already resolved")
	}
	if subtle.ConstantTimeCompare(
		[]byte(s.pendingApproval.request.Fingerprint),
		[]byte(decision.Fingerprint),
	) != 1 {
		return errors.New("invalid approval fingerprint")
	}
	if decision.Decision != ApprovalApprove && decision.Decision != ApprovalDeny && decision.Decision != ApprovalDenyWithReason {
		return errors.New("invalid approval decision")
	}

	pending := s.pendingApproval
	s.pendingApproval = nil
	pending.decision <- decision
	return nil
}

func (s *InfaiAgentSession) handleSubagentComm(context.Context, comms.AgentComm) error {
	panic("not implemented yet")
}

func (s *InfaiAgentSession) registerNewParentAgent(systemPrompt string) (*agent.Agent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	s.agentMapping[id] = ds.NewSet[uuid.UUID]()
	if err := s.agentComms.RegisterAgent(id); err != nil {
		return nil, err
	}
	s.Agents[id], err = agent.NewAgent(
		id,
		systemPrompt,
		agent.WithMaxTurns(100),
		agent.WithTools(s.availableTools...),
		agent.WithIAC(s.agentComms.IACChannel(id)),
		agent.WithCompactionCheck(s.shouldCompact),
	)
	if err != nil {
		return nil, err
	}
	s.Agents[id].SetModel(s.model)
	return s.Agents[id], nil
}

func (s *InfaiAgentSession) registerTransientAgent(id uuid.UUID, systemPrompt string) (*agent.Agent, error) {
	transient, err := agent.NewAgent(id, systemPrompt, agent.WithMaxTurns(1), agent.WithIAC(s.agentComms.IACChannel(id)))
	if err != nil {
		return nil, err
	}
	s.agentMapping[id] = ds.NewSet[uuid.UUID]()
	if err := s.agentComms.RegisterAgent(id); err != nil {
		return nil, err
	}
	s.Agents[id] = transient
	return transient, nil
}

func (s *InfaiAgentSession) removeAgent(id uuid.UUID) {
	delete(s.Agents, id)
	delete(s.agentMapping, id)
	s.agentComms.UnregisterAgent(id)
}

// lastAssistantText extracts the agent's answer from the full history. It
// lives here (the API boundary) because the wire format wants a plain reply
// string — the agent itself only returns history.
func lastAssistantText(messages []contracts.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Text()
		}
	}
	return ""
}

// lastAssistantReasoning extracts the reasoning text of the final assistant
// message, or "" when the provider returned none.
func lastAssistantReasoning(messages []contracts.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if m := messages[i]; m.Role == "assistant" && m.ReasoningContent != "" {
			return m.ReasoningContent
		}
	}
	return ""
}

func timelineHistory(timeline *store.Timeline, events []store.Event) ([]contracts.ChatMessage, error) {
	// Timeline loads events lazily so inspection stays cheap. Resuming a
	// session is the boundary where blob-backed records must become messages.
	records, err := timelineRecords(timeline, events)
	if err != nil {
		return nil, err
	}
	var history []contracts.ChatMessage
	for _, record := range records {
		switch record.Kind {
		case store.KindMessage:
			if record.Message != nil {
				if record.Message.Role == "system" {
					continue
				}
				history = append(history, *record.Message)
			}
		case store.KindCompaction:
			if record.Compaction != nil {
				history = append(history, contracts.NewUserMessage("<context-summary>\n"+record.Compaction.Summary+"\n</context-summary>"))
			}
		}
	}
	return history, nil
}

func timelineRecords(timeline *store.Timeline, events []store.Event) ([]store.Record, error) {
	records := make([]store.Record, 0, len(events))
	for _, event := range events {
		record := event.Record
		if record == nil {
			resolved, err := timeline.ResolveRecord(event)
			if err != nil {
				return nil, err
			}
			record = &resolved
		}
		records = append(records, *record)
	}
	return records, nil
}
