package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// ChatResult is the outcome of one Chat call on a session.
type ChatResult struct {
	SessionID        uuid.UUID
	Status           agent.TurnStatus
	Reply            string
	ReasoningContent string
	Pending          *ApprovalRequest
	Usage            *contracts.TokenUsage
	ContextTokens    uint64
}

// ChatOptions carries per-chat knobs.
type ChatOptions struct {
	Thinking contracts.InfaiThinkingLevel
}

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrEngineShuttingDown = errors.New("engine is shutting down")
)

// InfaiAgentEngine owns the provider registry, the on-disk session store and
// the set of active sessions. Sessions are created, stay registered in an
// idle state (no goroutine held), and are reused by Chat until CloseSession or
// Shutdown.
type InfaiAgentEngine struct {
	bgLogger  *slog.Logger
	engineCfg *config.AgentEngineConfig

	providers    contracts.LLMProviders
	sessionStore *store.SessionStore

	mu     sync.Mutex
	active map[uuid.UUID]*InfaiAgentSession

	providerAuth *providerAuthOperation

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	sessionStore, err := store.OpenSessionStore()
	if err != nil {
		return nil, err
	}
	engine, err := NewInfaiAgentEngineAt(bgLogger, sessionStore)
	if err != nil {
		return nil, err
	}
	engine.engineCfg = cfg

	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Minute, fmt.Errorf("toke > 1minute to get provider configs"))
	defer cancel()
	loadingProviderErr := make(chan error, 1)

	go func() {
		loadingProviderErr <- engine.LoadConfiguredProviders(ctx)
	}()

	select {
	case <-ctx.Done():
		bgLogger.ErrorContext(ctx, "Failed to get LoadConfiguredProviders", "reason", context.Cause(ctx))
		return nil, context.Cause(ctx)
	case errChan := <-loadingProviderErr:
		if errChan != nil {
			bgLogger.ErrorContext(ctx, "Failed to get LoadConfiguredProviders", "reason", errChan)
			return nil, errChan
		}
	}

	return engine, nil
}

// NewInfaiAgentEngineAt wires an engine to explicit stores. The harness uses
// the config-driven constructor; tests inject sandboxed stores here.
func NewInfaiAgentEngineAt(bgLogger *slog.Logger, sessionStore *store.SessionStore) (*InfaiAgentEngine, error) {
	if sessionStore == nil {
		return nil, errors.New("engine: stores required")
	}

	return &InfaiAgentEngine{
		bgLogger:     bgLogger,
		providers:    contracts.LLMProviders{Providers: make(map[string]contracts.LLMProviderConfiguration)},
		sessionStore: sessionStore,
		active:       make(map[uuid.UUID]*InfaiAgentSession),
		stopCh:       make(chan struct{}),
	}, nil
}

// ---- Sessions ----

type CreateSessionOptions struct {
	Provider string
	Model    string
	Cwd      string
}

// CreateSession registers a new idle session and persists it. It stays until
// CloseSession or Shutdown; a prompt can be sent any number of times via Chat.
func (e *InfaiAgentEngine) CreateSession(ctx context.Context, opts CreateSessionOptions) (*InfaiAgentSession, error) {
	select {
	case <-e.stopCh:
		return nil, ErrEngineShuttingDown
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

	providerConfig, ok := e.Provider(opts.Provider)
	if !ok {
		return nil, fmt.Errorf("engine: provider %q not configured", opts.Provider)
	}
	modelConfig, ok := providerConfig.Models[opts.Model]
	if !ok {
		return nil, fmt.Errorf("engine: model %q not configured for provider %q", opts.Model, opts.Provider)
	}

	sess, err := NewSession(
		e.bgLogger.WithGroup("session"),
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
	)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.active[sess.sessionID] = sess
	e.mu.Unlock()

	e.bgLogger.Info("session created", "session_id", sess.sessionID, "provider", opts.Provider, "model", modelConfig.Id)
	return sess, nil
}

// LoadSession rebuilds a saved session from its timeline and registers it
// as active so it can be chatted with again.
func (e *InfaiAgentEngine) LoadSession(id uuid.UUID) (*InfaiAgentSession, error) {
	select {
	case <-e.stopCh:
		return nil, ErrEngineShuttingDown
	default:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if sess, ok := e.active[id]; ok {
		return sess, nil
	}
	meta, err := e.sessionStore.LoadMeta(id)
	if err != nil {
		return nil, err
	}

	timeline, err := e.sessionStore.LoadSessionTimelineClient(id)
	if err != nil {
		return nil, err
	}

	events, err := timeline.LoadActiveBranchContext()
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}
	history, err := timelineHistory(timeline, events)
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}

	providerConfig, ok := e.providers.Providers[meta.Provider]
	if !ok {
		_ = timeline.Close()
		return nil, ErrNoProvider
	}
	modelConfig, ok := providerConfig.Models[meta.Model]
	if !ok {
		return nil, fmt.Errorf("engine: model %q not configured for provider %q", meta.Model, meta.Provider)
	}

	sess, err := NewResumedSession(
		e.bgLogger.WithGroup("session"),
		contracts.NewProvisionedModel(
			providerConfig.Id,
			meta.Provider,
			providerConfig.BaseEndpoint,
			providerConfig.APIType,
			providerConfig.Auth,
			modelConfig,
		),
		meta, history, timeline, e.sessionStore)
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}

	e.active[id] = sess

	e.bgLogger.Info("session loaded", "session_id", id, "provider", meta.Provider)
	return sess, nil
}

// Session returns an active session, if any.
func (e *InfaiAgentEngine) Session(id uuid.UUID) (*InfaiAgentSession, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.active[id]
	return sess, ok
}

// SetSessionModel switches an active session to another provider's model. An
// empty modelName keeps the provider's configured model.
func (e *InfaiAgentEngine) SetSessionModel(id uuid.UUID, providerName, modelId string) (*InfaiAgentSession, error) {
	e.mu.Lock()
	sess, ok := e.active[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	providerConfig, ok := e.Provider(providerName)
	if !ok {
		return nil, ErrNoProvider
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

// Chat runs one prompt against an existing session and returns the outcome.
func (e *InfaiAgentEngine) Chat(ctx context.Context, id uuid.UUID, prompt string, opts ChatOptions) (*ChatResult, error) {
	e.mu.Lock()
	sess, ok := e.active[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	needsRefresh, err := sess.providerAuthNeedsRefresh(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if needsRefresh {
		if err := e.refreshSessionProviderAuth(ctx, sess); err != nil {
			return nil, err
		}
	}
	if sess.CurrentThinkingPattern() != opts.Thinking {
		if err := sess.SetThinkingPattern(opts.Thinking); err != nil {
			return nil, err
		}
	}
	return sess.Chat(ctx, prompt, opts)
}

func (e *InfaiAgentEngine) refreshSessionProviderAuth(ctx context.Context, sess *InfaiAgentSession) error {
	providerName := sess.Meta().Provider
	e.mu.Lock()
	defer e.mu.Unlock()

	provider, ok := e.providers.Providers[providerName]
	if !ok {
		e.mu.Unlock()
		return ErrNoProvider
	}
	refreshed, changed, err := models.RefreshProviderAuth(ctx, provider.Id, provider.Auth)
	if err != nil {
		return err
	}
	if !changed {
		return sess.setProviderAuth(providerName, provider.Auth)
	}

	provider.Auth = refreshed
	candidate := providersWith(e.providers, providerName, provider)
	if err := store.PersistProviders(candidate); err != nil {
		return fmt.Errorf("persist refreshed provider credentials: %w", err)
	}

	e.providers = candidate

	return sess.setProviderAuth(providerName, refreshed)
}

func (e *InfaiAgentEngine) ResolveApproval(id uuid.UUID, approvalID uuid.UUID, decision ApprovalDecisionFromClient) error {
	sess, ok := e.Session(id)
	if !ok {
		return ErrSessionNotFound
	}
	return sess.ResolveApproval(approvalID, decision)
}

// CompactSession creates a continuation checkpoint for an active session.
func (e *InfaiAgentEngine) CompactSession(ctx context.Context, id uuid.UUID) error {
	e.mu.Lock()
	sess, ok := e.active[id]
	e.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	return sess.CompactChat(ctx)
}

// CloseSession removes and closes a session and deletes its timeline from
// disk. An in-flight Chat finishes or is canceled by its own context.
func (e *InfaiAgentEngine) CloseSession(id uuid.UUID) error {
	e.mu.Lock()
	sess, ok := e.active[id]
	if ok {
		delete(e.active, id)
	}
	e.mu.Unlock()

	if !ok {
		return ErrSessionNotFound
	}
	sess.close()
	if err := e.sessionStore.Delete(id); err != nil {
		return err
	}
	e.bgLogger.Info("session closed", "session_id", id)
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
	sess, ok := e.active[id]
	e.mu.Unlock()
	if ok {
		if err := sess.Rename(name); err != nil {
			return nil, err
		}
		return &sess.meta, nil
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
		_, active := e.active[meta.ID]
		summaries = append(summaries, contracts.SessionSummary{
			ID: meta.ID, Name: meta.Name, Provider: meta.Provider, Model: meta.Model,
			Cwd: meta.Cwd, CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, Active: active,
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
	records, err := timelineRecords(timeline, events)
	return meta, records, err
}

func (e *InfaiAgentEngine) GetTimeline(id uuid.UUID) (store.SessionMeta, []store.Event, uuid.UUID, error) {
	if sess, ok := e.Session(id); ok {
		events, head, err := sess.Timeline()
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
		return contracts.TaskChecklistState{}, ErrSessionNotFound
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
	if e.providerAuth != nil && e.providerAuth.result.Status == contracts.ProviderAuthPending {
		e.providerAuth.cancel()
	}
	for id, sess := range e.active {
		sess.close()
		delete(e.active, id)
	}
	e.mu.Unlock()

	e.bgLogger.DebugContext(ctx, "engine shutdown complete")
	return nil
}
