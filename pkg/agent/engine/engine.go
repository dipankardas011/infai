package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/session"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// InfaiAgentEngine owns the provider registry, the on-disk session store and
// the set of active sessions. Sessions are created, stay registered in an
// idle state (no goroutine held), and are reused by Chat until CloseSession or
// Shutdown.
type InfaiAgentEngine struct {
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig
	ctx       context.Context
	cancel    context.CancelCauseFunc

	providerMu   sync.Mutex
	providers    contracts.LLMProviders
	sessionStore *store.SessionStore

	mu                  sync.Mutex
	activeSessionAgents map[uuid.UUID]*session.InfaiAgentSession
	aseComms            *comms.AgentComms
	children            map[uuid.UUID]map[uuid.UUID]comms.AgentCommKind

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewInfaiAgentEngine(parent context.Context, bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	sessionStore, err := store.OpenSessionStore()
	if err != nil {
		return nil, err
	}

	eCtx, eCancel := context.WithCancelCause(parent)
	engine := &InfaiAgentEngine{
		bgLogger:            bgLogger,
		ctx:                 eCtx,
		cancel:              eCancel,
		providers:           contracts.LLMProviders{Providers: make(map[string]contracts.LLMProviderConfiguration)},
		sessionStore:        sessionStore,
		activeSessionAgents: make(map[uuid.UUID]*session.InfaiAgentSession),
		children:            make(map[uuid.UUID]map[uuid.UUID]comms.AgentCommKind),
		stopCh:              make(chan struct{}),
		aseComms:            comms.NewAgentComms(),
		engineCfg:           cfg,
	}

	go engine.listenForAgentComms()

	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Minute, fmt.Errorf("toke > 1minute to get provider configs"))
	defer cancel()
	loadingProviderErr := make(chan error, 1)

	go func() {
		loadingProviderErr <- engine.LoadConfiguredProviders(ctx)
	}()

	select {
	case <-ctx.Done():
		engine.cancel(context.Cause(ctx))
		engine.aseComms.Close()
		bgLogger.ErrorContext(ctx, "Failed to get LoadConfiguredProviders", "reason", context.Cause(ctx))
		return nil, context.Cause(ctx)
	case errChan := <-loadingProviderErr:
		if errChan != nil {
			engine.cancel(errChan)
			engine.aseComms.Close()
			bgLogger.ErrorContext(ctx, "Failed to get LoadConfiguredProviders", "reason", errChan)
			return nil, errChan
		}
	}

	return engine, nil
}

// ---- Sessions ----

func (e *InfaiAgentEngine) listenForAgentComms() {
	for {
		msg, err := e.aseComms.SubscribeForSessionAgentsEvents(e.ctx)
		if err != nil {
			e.bgLogger.ErrorContext(e.ctx, "failure in subscribe for sessionagentEvents", "error", err)
			return
		}
		switch msg.Kind {
		case comms.AgentCommKindSpawnSidecar, comms.AgentCommKindSpawnSidecarBackground:
			go e.spawnSidecar(msg)
		case comms.AgentCommKindResultSidecar:
			e.relaySidecarResult(msg)
		default:
			e.bgLogger.Warn("unhandled agent comm", "kind", msg.Kind, "from", msg.From)
		}
	}
}

func (e *InfaiAgentEngine) spawnSidecar(msg *comms.AgentComm) {
	var request comms.DelegationToSidecarLoop
	err := json.Unmarshal(msg.Payload, &request)
	if err != nil {
		e.bgLogger.ErrorContext(e.ctx, "failed to unmarshal for spawnSidecar event in engine", "error", err)
		return
	}

	if request.ParentID != msg.From {
		err = errors.New("delegation parent does not match sender")
	}

	var childID uuid.UUID
	if err == nil {
		childID, err = e.createSidecarSession(msg.From, request, msg.Kind)
	}

	decision := comms.DelegationConformation{DelegatedTo: childID}
	if err != nil {
		decision.Err = err.Error()
	}

	payload, marshalErr := json.Marshal(decision)
	if marshalErr != nil {
		e.bgLogger.Error("encode delegation decision", "error", marshalErr)
		return
	}

	if sendErr := e.aseComms.SendToSessionAgent(
		e.ctx,
		msg.From,
		&comms.AgentComm{
			Kind:    comms.AgentCommDelegationConformation,
			To:      msg.From,
			Payload: payload,
		},
	); sendErr != nil {
		e.bgLogger.Warn("deliver delegation decision", "parent", msg.From, "error", sendErr)
	}
}

func (e *InfaiAgentEngine) createSidecarSession(parentID uuid.UUID, request comms.DelegationToSidecarLoop, kind comms.AgentCommKind) (uuid.UUID, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	select {
	case <-e.stopCh:
		return uuid.Nil, harnessErr.ErrEngineShuttingDown
	default:
	}

	parent, ok := e.activeSessionAgents[parentID]
	if !ok {
		return uuid.Nil, errors.New("delegation parent is not active")
	}
	parentMeta := parent.Meta()
	if parentMeta.AgentKind != contracts.InteractiveAgent || parent.Status() == contracts.SessionTombstone {
		return uuid.Nil, errors.New("delegation parent is not active")
	}

	engineLLMProviders, ok := e.LLMProvider(parentMeta.Provider)
	if !ok {
		return uuid.Nil, harnessErr.ErrNoProvider
	}

	model, ok := engineLLMProviders.Models[parentMeta.Model]
	if !ok {
		return uuid.Nil, fmt.Errorf("engine: model %q not configured for provider %q", parentMeta.Model, parentMeta.Provider)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}

	if err := e.aseComms.RegisterSessionAgent(id); err != nil {
		return uuid.Nil, err
	}
	cwd := request.Cwd
	if cwd == "" {
		cwd = parentMeta.Cwd
	}
	child, err := session.NewSession(
		e.ctx,
		id,
		parentID,
		e.bgLogger.WithGroup("session"),
		&request.Task,
		&request.AcceptanceScript,
		request.MaxTurns,
		contracts.NewProvisionedModel(
			engineLLMProviders.Id,
			parentMeta.Provider,
			engineLLMProviders.BaseEndpoint,
			engineLLMProviders.APIType,
			engineLLMProviders.Auth,
			model,
		),
		cwd,
		e.sessionStore,
		e.aseComms.NewSessionAgentComms(id),
		contracts.SidecarLoopAgent,
	)
	if err != nil {
		e.aseComms.UnregisterSessionAgent(id)
		return uuid.Nil, err
	}

	parentMeta.Offsprings = append(parentMeta.Offsprings, id)
	if err := e.sessionStore.SaveMeta(parentMeta); err != nil {
		child.Close()
		e.aseComms.UnregisterSessionAgent(id)
		return uuid.Nil, err
	}
	parent.AddOffspring(id)
	e.activeSessionAgents[id] = child
	if e.children[parentID] == nil {
		e.children[parentID] = make(map[uuid.UUID]comms.AgentCommKind)
	}
	e.children[parentID][id] = kind
	return id, nil
}

func (e *InfaiAgentEngine) relaySidecarResult(msg *comms.AgentComm) {
	e.mu.Lock()
	_, isParentOnline := e.activeSessionAgents[msg.To]
	kind, isChildOnline := e.children[msg.To][msg.From]
	e.mu.Unlock()

	if !isParentOnline || !isChildOnline {
		e.bgLogger.Warn("dropping sidecar result without active parent", "parent", msg.To, "child", msg.From)
		return
	}

	resultKind := comms.AgentCommKindResultSidecar
	if kind == comms.AgentCommKindSpawnSidecarBackground {
		resultKind = comms.AgentCommKindResultSidecarBackground
	}

	if err := e.aseComms.SendToSessionAgent(
		e.ctx,
		msg.To,
		&comms.AgentComm{
			From:    msg.From,
			To:      msg.To,
			Kind:    resultKind,
			Payload: msg.Payload,
		},
	); err != nil {
		e.bgLogger.Warn("deliver sidecar result", "parent", msg.To, "child", msg.From, "error", err)
	}
}

type CreateSessionOptions struct {
	Provider  string
	Model     string
	Cwd       string
	AgentKind contracts.AgentKind
}

// CreateSession registers a new idle session and persists it. It stays until
// CloseSession or Shutdown; a prompt can be sent any number of times via Chat.
func (e *InfaiAgentEngine) CreateSession(ctx context.Context, opts CreateSessionOptions) (*session.InfaiAgentSession, error) {
	select {
	case <-e.stopCh:
		return nil, harnessErr.ErrEngineShuttingDown
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if opts.Provider == "" {
		return nil, errors.New("engine: provider is required")
	}
	if opts.Model == "" {
		return nil, errors.New("engine: model is required")
	}

	if opts.AgentKind == "" {
		opts.AgentKind = contracts.InteractiveAgent
	}

	providerConfig, ok := e.LLMProvider(opts.Provider)
	if !ok {
		return nil, fmt.Errorf("engine: provider %q not configured", opts.Provider)
	}
	modelConfig, ok := providerConfig.Models[opts.Model]
	if !ok {
		return nil, fmt.Errorf("engine: model %q not configured for provider %q", opts.Model, opts.Provider)
	}
	sessID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if err := e.aseComms.RegisterSessionAgent(sessID); err != nil {
		return nil, err
	}

	sess, err := session.NewSession(
		e.ctx,
		sessID,
		uuid.Nil,
		e.bgLogger.WithGroup("session"),
		nil,
		nil,
		1000,
		contracts.NewProvisionedModel(
			providerConfig.Id,
			opts.Provider,
			providerConfig.BaseEndpoint,
			providerConfig.APIType,
			providerConfig.Auth,
			modelConfig,
		),
		opts.Cwd,
		e.sessionStore,
		e.aseComms.NewSessionAgentComms(sessID),
		opts.AgentKind,
	)
	if err != nil {
		e.aseComms.UnregisterSessionAgent(sessID)
		return nil, err
	}

	e.mu.Lock()
	select {
	case <-e.stopCh:
		e.mu.Unlock()
		sess.Close()
		e.aseComms.UnregisterSessionAgent(sessID)
		_ = e.sessionStore.Delete(sessID)
		return nil, harnessErr.ErrEngineShuttingDown
	default:
		e.activeSessionAgents[sessID] = sess
	}
	e.mu.Unlock()

	e.bgLogger.Info("session created", "session_id", sessID, "provider", opts.Provider, "model", modelConfig.Id)
	return sess, nil
}

// LoadSession rebuilds a saved session from its timeline and registers it
// as active so it can be chatted with again.
func (e *InfaiAgentEngine) LoadSession(sessionID uuid.UUID) (*session.InfaiAgentSession, error) {
	select {
	case <-e.stopCh:
		return nil, harnessErr.ErrEngineShuttingDown
	default:
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if sess, ok := e.activeSessionAgents[sessionID]; ok {
		return sess, nil
	}
	meta, err := e.sessionStore.LoadMeta(sessionID)
	if err != nil {
		return nil, err
	}

	timeline, err := e.sessionStore.LoadSessionTimelineClient(sessionID)
	if err != nil {
		return nil, err
	}

	events, err := timeline.LoadActiveBranchContext()
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}
	history, err := session.TimelineHistory(timeline, events)
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}

	providerConfig, ok := e.LLMProvider(meta.Provider)
	if !ok {
		_ = timeline.Close()
		return nil, harnessErr.ErrNoProvider
	}
	modelConfig, ok := providerConfig.Models[meta.Model]
	if !ok {
		return nil, fmt.Errorf("engine: model %q not configured for provider %q", meta.Model, meta.Provider)
	}
	if err := e.aseComms.RegisterSessionAgent(sessionID); err != nil {
		_ = timeline.Close()
		return nil, err
	}

	sess, err := session.NewResumedSession(
		e.ctx,
		e.bgLogger.WithGroup("session"),
		contracts.NewProvisionedModel(
			providerConfig.Id,
			meta.Provider,
			providerConfig.BaseEndpoint,
			providerConfig.APIType,
			providerConfig.Auth,
			modelConfig,
		),
		meta, history, timeline, e.sessionStore,
		e.aseComms.NewSessionAgentComms(sessionID),
	)
	if err != nil {
		e.aseComms.UnregisterSessionAgent(sessionID)
		_ = timeline.Close()
		return nil, err
	}

	select {
	case <-e.stopCh:
		sess.Close()
		e.aseComms.UnregisterSessionAgent(sessionID)
		return nil, harnessErr.ErrEngineShuttingDown
	default:
		e.activeSessionAgents[sessionID] = sess
	}

	e.bgLogger.Info("session loaded", "session_id", sessionID, "provider", meta.Provider)
	return sess, nil
}

// Session returns an active session, if any.
func (e *InfaiAgentEngine) Session(id uuid.UUID) (*session.InfaiAgentSession, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.activeSessionAgents[id]
	return sess, ok
}

// SetSessionModel switches an active session to another provider's model. An
// empty modelName keeps the provider's configured model.
func (e *InfaiAgentEngine) SetSessionModel(id uuid.UUID, providerName, modelId string) (*session.InfaiAgentSession, error) {
	e.mu.Lock()
	sess, ok := e.activeSessionAgents[id]
	e.mu.Unlock()
	if !ok {
		return nil, harnessErr.ErrSessionNotFound
	}
	providerConfig, ok := e.LLMProvider(providerName)
	if !ok {
		return nil, harnessErr.ErrNoProvider
	}
	if modelId == "" {
		return nil, errors.New("engine: model is required")
	}

	modelConfig, ok := providerConfig.Models[modelId]
	if !ok {
		return nil, fmt.Errorf("engine: model %q not configured for provider %q", modelId, providerName)
	}

	if err := sess.SetModel(contracts.NewProvisionedModel(
		providerConfig.Id,
		providerName,
		providerConfig.BaseEndpoint,
		providerConfig.APIType,
		providerConfig.Auth,
		modelConfig,
	)); err != nil {
		return nil, err
	}

	e.bgLogger.Info("session model set", "session_id", id, "provider", providerName, "model", modelId)
	return sess, nil
}

// Chat validates and enqueues one prompt. Session execution continues on the
// engine-owned lifecycle context after this request returns.
func (e *InfaiAgentEngine) Chat(ctx context.Context, id uuid.UUID, input contracts.UserInput, opts contracts.ChatOptions) error {
	select {
	case <-e.stopCh:
		return harnessErr.ErrEngineShuttingDown
	default:
	}
	e.mu.Lock()
	sess, ok := e.activeSessionAgents[id]
	e.mu.Unlock()
	if !ok {
		return harnessErr.ErrSessionNotFound
	}

	if needsRefresh, err := sess.ProviderAuthNeedsRefresh(time.Now().UTC()); err != nil {
		return err
	} else if needsRefresh {
		if err := e.refreshSessionProviderAuth(ctx, sess); err != nil {
			return err
		}
	}

	if sess.CurrentThinkingPattern() != opts.Thinking {
		if err := sess.SetThinkingPattern(opts.Thinking); err != nil {
			return err
		}
	}

	if err := sess.EnqueueUserMessage(ctx, input); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			meta := sess.Meta()
			e.bgLogger.ErrorContext(ctx, "session chat enqueue failed", "session_id", id, "provider", meta.Provider, "model", meta.Model, "error", err)
		}
		return err
	}
	return nil
}

func (e *InfaiAgentEngine) refreshSessionProviderAuth(ctx context.Context, sess *session.InfaiAgentSession) error {
	providerName := sess.Meta().Provider
	e.providerMu.Lock()
	defer e.providerMu.Unlock()

	provider, ok := e.providers.Providers[providerName]
	if !ok {
		return harnessErr.ErrNoProvider
	}
	refreshed, changed, err := models.RefreshProviderAuth(ctx, provider.Id, provider.Auth)
	if err != nil {
		e.bgLogger.WarnContext(ctx, "provider credential refresh failed", "provider", providerName, "error", err)
		return err
	}
	if !changed {
		return sess.SetProviderAuth(providerName, provider.Auth)
	}

	provider.Auth = refreshed
	candidate := providersWith(e.providers, providerName, provider)
	if err := store.PersistProviders(candidate); err != nil {
		e.bgLogger.ErrorContext(ctx, "persist refreshed provider credentials", "provider", providerName, "error", err)
		return fmt.Errorf("persist refreshed provider credentials: %w", err)
	}

	e.providers = candidate
	e.bgLogger.InfoContext(ctx, "provider credentials refreshed", "provider", providerName)

	return sess.SetProviderAuth(providerName, refreshed)
}

func (e *InfaiAgentEngine) SubscribeEvents(id uuid.UUID) (glue.SessionView, <-chan contracts.EventStream, func(), error) {
	select {
	case <-e.stopCh:
		return glue.SessionView{}, nil, nil, harnessErr.ErrEngineShuttingDown
	default:
	}
	sess, ok := e.Session(id)
	if !ok {
		return glue.SessionView{}, nil, nil, harnessErr.ErrSessionNotFound
	}
	return sess.JoinSessionEvents()
}

func (e *InfaiAgentEngine) ResolveApproval(id uuid.UUID, approvalID uuid.UUID, decision contracts.ApprovalConclusion) error {
	sess, ok := e.Session(id)
	if !ok {
		return harnessErr.ErrSessionNotFound
	}
	return sess.ResolveApproval(approvalID, decision)
}

func (e *InfaiAgentEngine) CancelTurn(id uuid.UUID) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	sess, ok := e.activeSessionAgents[id]
	if !ok {
		return harnessErr.ErrSessionNotFound
	}
	if meta := sess.Meta(); meta.AgentKind == contracts.SidecarLoopAgent {
		return fmt.Errorf("%w: sidecar_loop session cannot be canceled directly; cancel its caller session %s", harnessErr.ErrInvalidInput, meta.ParentID)
	}
	if sess.Status() != contracts.SessionBusy && sess.Status() != contracts.SessionWaitingApproval {
		return harnessErr.ErrNoTurnToCancel
	}
	for childID, kind := range e.children[id] {
		if kind == comms.AgentCommKindSpawnSidecar {
			e.cancelSidecarTree(childID)
		}
	}
	return sess.CancelTurn()
}

func (e *InfaiAgentEngine) cancelSidecarTree(id uuid.UUID) {
	sess, ok := e.activeSessionAgents[id]
	if !ok {
		return
	}
	for childID := range e.children[id] {
		e.cancelSidecarTree(childID)
	}
	if sess.Status() != contracts.SessionCompleted && sess.Status() != contracts.SessionMaxIterationExhausted && sess.Status() != contracts.SessionTombstone {
		if sess.Meta().AgentKind == contracts.SidecarLoopAgent {
			sess.CloseWithReason("parent turn canceled")
		} else {
			sess.Close()
		}
		e.aseComms.UnregisterSessionAgent(id)
		delete(e.activeSessionAgents, id)
	}
}

// CompactSession creates a continuation checkpoint for an active session.
func (e *InfaiAgentEngine) CompactSession(ctx context.Context, id uuid.UUID) error {
	e.mu.Lock()
	sess, ok := e.activeSessionAgents[id]
	e.mu.Unlock()
	if !ok {
		return harnessErr.ErrSessionNotFound
	}
	return sess.ManualCompactChat(ctx)
}

func (e *InfaiAgentEngine) CloseSession(id uuid.UUID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.activeSessionAgents[id]
	if !ok {
		return harnessErr.ErrSessionNotFound
	}
	if meta := sess.Meta(); meta.AgentKind == contracts.SidecarLoopAgent {
		return fmt.Errorf("%w: sidecar_loop session cannot be closed directly; close its caller session %s", harnessErr.ErrInvalidInput, meta.ParentID)
	}
	e.closeSessionTree(id)
	return nil
}

// closeSessionTree is called with e.mu held so a new child cannot appear
// between closing descendants and closing their parent.
func (e *InfaiAgentEngine) closeSessionTree(id uuid.UUID) {
	sess, ok := e.activeSessionAgents[id]
	if !ok {
		return
	}
	for childID := range e.children[id] {
		e.closeSessionTree(childID)
	}
	delete(e.children, id)
	sess.Close()
	e.aseComms.UnregisterSessionAgent(id)
	delete(e.activeSessionAgents, id)
	e.bgLogger.Info("session closed", "session_id", id)
}

// DeleteSession closes the session when it is resident, then removes its
// timeline and metadata from disk. A saved session has nothing to close.
func (e *InfaiAgentEngine) DeleteSession(id uuid.UUID) error {
	switch err := e.CloseSession(id); {
	case err == nil:
	case errors.Is(err, harnessErr.ErrSessionNotFound):
		if _, loadErr := e.sessionStore.LoadMeta(id); loadErr != nil {
			return harnessErr.ErrSessionNotFound
		}
	default:
		return err
	}

	if err := e.sessionStore.Delete(id); err != nil {
		return err
	}
	e.bgLogger.Info("session deleted", "session_id", id)
	return nil
}

// RenameSession updates the persisted display name of a session, whether it is
// currently loaded or not. It returns the updated metadata.
func (e *InfaiAgentEngine) RenameSession(id uuid.UUID, name string) (*store.SessionMeta, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("engine: session name is required")
	}
	e.mu.Lock()
	sess, ok := e.activeSessionAgents[id]
	e.mu.Unlock()
	if ok {
		if err := sess.Rename(name); err != nil {
			return nil, err
		}
		meta := sess.Meta()
		return &meta, nil
	}
	meta, err := e.sessionStore.LoadMeta(id)
	if err != nil {
		return nil, err
	}
	meta.Name = name
	meta.UpdatedAt = time.Now().UTC()
	if err := e.sessionStore.SaveMeta(meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// ListSessions returns every saved session's meta (active or not), newest
// first.
func (e *InfaiAgentEngine) ListSessions() []contracts.SessionSummary {
	metas, err := e.sessionStore.List()
	if err != nil {
		e.bgLogger.Debug("list sessions failed", "error", err)
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	summaries := make([]contracts.SessionSummary, 0, len(metas))
	for _, meta := range metas {
		sess, active := e.activeSessionAgents[meta.ID]
		// A session that is not resident has no runtime state left to report, so
		// it reports the conclusion it recorded, and concludes otherwise.
		status := contracts.SessionTombstone
		if active {
			status = sess.Status()
		} else if meta.Conclusion != nil {
			status = meta.Conclusion.Status
		}
		summaries = append(summaries, contracts.SessionSummary{
			ID:        meta.ID,
			Name:      meta.Name,
			Provider:  meta.Provider,
			Model:     meta.Model,
			Cwd:       meta.Cwd,
			CreatedAt: meta.CreatedAt,
			UpdatedAt: meta.UpdatedAt,
			Active:    active,
			Status:    status,
			AgentKind: meta.AgentKind,
		})
	}
	return summaries
}

// GetSessionRecords returns a session's meta plus its active timeline records.
func (e *InfaiAgentEngine) GetSessionRecords(id uuid.UUID) (store.SessionMeta, []store.Record, error) {
	meta, err := e.sessionStore.LoadMeta(id)
	if err != nil {
		return store.SessionMeta{}, nil, err
	}

	timeline, err := e.sessionStore.LoadSessionTimelineClient(id)
	if err != nil {
		return store.SessionMeta{}, nil, err
	}
	defer timeline.Close()

	events, err := timeline.LoadActiveBranchContext()
	if err != nil {
		return store.SessionMeta{}, nil, err
	}
	records, err := session.CompleteResolveRawTimelineEventsToRecords(timeline, events)
	return meta, records, err
}

func (e *InfaiAgentEngine) GetTimeline(id uuid.UUID) (store.SessionMeta, []store.Event, uuid.UUID, error) {
	if sess, ok := e.Session(id); ok {
		events, head, err := sess.ViewTimeline()
		if err != nil {
			return store.SessionMeta{}, nil, uuid.Nil, err
		}
		return sess.Meta(), events, head, nil
	}
	meta, err := e.sessionStore.LoadMeta(id)
	if err != nil {
		return store.SessionMeta{}, nil, uuid.Nil, err
	}

	timeline, err := e.sessionStore.LoadSessionTimelineClient(id)
	if err != nil {
		return store.SessionMeta{}, nil, uuid.Nil, err
	}
	defer timeline.Close()
	events, err := timeline.LoadEntireTimeline()
	if err != nil {
		return store.SessionMeta{}, nil, uuid.Nil, err
	}
	return meta, events, timeline.CurrentHeadEventID(), err
}

func (e *InfaiAgentEngine) SelectBranch(id, eventID uuid.UUID) (contracts.TaskChecklistState, error) {
	sess, ok := e.Session(id)
	if !ok {
		return contracts.TaskChecklistState{}, harnessErr.ErrSessionNotFound
	}
	return sess.SelectBranch(eventID)
}

// Shutdown stops accepting sessions and closes every registered one.
func (e *InfaiAgentEngine) Shutdown(ctx context.Context) error {
	e.bgLogger.DebugContext(ctx, "received shutdown request")

	e.stopOnce.Do(func() {
		close(e.stopCh)
	})
	e.mu.Lock()
	for id, sess := range e.activeSessionAgents {
		if sess.Meta().ParentID == uuid.Nil {
			e.closeSessionTree(id)
		}
	}
	e.mu.Unlock()
	e.cancel(harnessErr.ErrEngineShuttingDown)
	e.aseComms.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	e.bgLogger.DebugContext(ctx, "engine shutdown complete")
	return nil
}
