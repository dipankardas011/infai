package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/agent"
	"github.com/dipankardas011/infai/pkg/agent/config"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
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
	ContextTokens    int
}

// ChatOptions carries per-chat knobs.
type ChatOptions struct{}

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

	_ *sql.DB

	modelProviderStore *store.ProviderStore
	sessionStore       *store.SessionStore

	mu     sync.Mutex
	active map[uuid.UUID]*InfaiAgentSession

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewInfaiAgentEngine(bgLogger *slog.Logger, cfg *config.AgentEngineConfig) (*InfaiAgentEngine, error) {
	providerStore, err := store.OpenProviderStore()
	if err != nil {
		return nil, err
	}
	sessionStore, err := store.OpenSessionStore()
	if err != nil {
		return nil, err
	}
	engine, err := NewInfaiAgentEngineAt(bgLogger, providerStore, sessionStore)
	if err != nil {
		return nil, err
	}
	engine.engineCfg = cfg
	return engine, nil
}

// NewInfaiAgentEngineAt wires an engine to explicit stores. The harness uses
// the config-driven constructor; tests inject sandboxed stores here.
func NewInfaiAgentEngineAt(bgLogger *slog.Logger, providerStore *store.ProviderStore, sessionStore *store.SessionStore) (*InfaiAgentEngine, error) {
	if providerStore == nil || sessionStore == nil {
		return nil, errors.New("engine: stores required")
	}
	return &InfaiAgentEngine{
		bgLogger:           bgLogger,
		modelProviderStore: providerStore,
		sessionStore:       sessionStore,
		active:             make(map[uuid.UUID]*InfaiAgentSession),
		stopCh:             make(chan struct{}),
	}, nil
}

// ---- Provider management ----

func (e *InfaiAgentEngine) ListProviders() []store.Provider {
	return e.modelProviderStore.List()
}

func (e *InfaiAgentEngine) Provider(name string) (store.Provider, bool) {
	return e.modelProviderStore.Get(name)
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
	p, ok := e.modelProviderStore.Get(opts.Provider)
	if !ok {
		return nil, fmt.Errorf("engine: provider %q not configured", opts.Provider)
	}
	if p.APIType != "" && p.APIType != "openai" {
		return nil, fmt.Errorf("engine: unsupported api_type %q for provider %q", p.APIType, p.Name)
	}
	if opts.Model == "" {
		return nil, errors.New("engine: model is required")
	}
	m, ok := p.Model(opts.Model)
	if !ok {
		return nil, fmt.Errorf("engine: provider %q has no model %q", p.Name, opts.Model)
	}
	ctxWindow := m.ContextWindow

	sess, err := NewSession(e.bgLogger.WithGroup("session"), &p, m.Name, ctxWindow, opts.Cwd, e.sessionStore)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.active[sess.sessionID] = sess
	e.mu.Unlock()

	e.bgLogger.Info("session created", "session_id", sess.sessionID, "provider", p.Name, "model", m.Name)
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

	p, ok := e.modelProviderStore.Get(meta.Provider)
	if !ok {
		_ = timeline.Close()
		return nil, ErrNoProvider
	}
	if model, ok := p.Model(meta.Model); ok {
		meta.ContextWindow = model.ContextWindow
	}

	sess, err := NewResumedSession(e.bgLogger.WithGroup("session"), &p, meta, history, timeline, e.sessionStore)
	if err != nil {
		_ = timeline.Close()
		return nil, err
	}

	e.active[id] = sess

	e.bgLogger.Info("session loaded", "session_id", id, "provider", p.Name)
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
func (e *InfaiAgentEngine) SetSessionModel(id uuid.UUID, providerName, modelName string) (*InfaiAgentSession, error) {
	e.mu.Lock()
	sess, ok := e.active[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	p, ok := e.modelProviderStore.Get(providerName)
	if !ok {
		return nil, ErrNoProvider
	}
	if p.APIType != "" && p.APIType != "openai" {
		return nil, fmt.Errorf("engine: unsupported api_type %q for provider %q", p.APIType, p.Name)
	}
	if modelName == "" {
		return nil, errors.New("engine: model is required")
	}
	m, ok := p.Model(modelName)
	if !ok {
		return nil, fmt.Errorf("engine: provider %q has no model %q", providerName, modelName)
	}
	sess.SetModel(&p, m.Name, m.ContextWindow)
	e.bgLogger.Info("session model set", "session_id", id, "provider", providerName, "model", modelName)
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
	return sess.Chat(ctx, prompt, opts)
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

func (e *InfaiAgentEngine) SelectBranch(id, eventID uuid.UUID) error {
	sess, ok := e.Session(id)
	if !ok {
		return ErrSessionNotFound
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
	for id, sess := range e.active {
		sess.close()
		delete(e.active, id)
	}
	e.mu.Unlock()

	e.bgLogger.DebugContext(ctx, "engine shutdown complete")
	return nil
}
