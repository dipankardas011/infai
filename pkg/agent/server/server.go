package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/engine"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/session"
	"github.com/google/uuid"
)

type Server struct {
	engine        *engine.InfaiAgentEngine
	httpSrv       *http.Server
	enableHealthz bool
	logger        *slog.Logger
}

// maxChatBodyBytes bounds the chat request body. Inline base64 images make it
// larger than a typical JSON endpoint, but still bounded well below the
// harness-wide total attachment limit plus envelope overhead.
const maxChatBodyBytes = 32 << 20

// contentTypeEventStream is what a server-sent-event response declares.
const contentTypeEventStream = "text/event-stream"

func New(l *slog.Logger, e *engine.InfaiAgentEngine, addr string, enableHealthz bool) *Server {
	s := &Server{engine: e, enableHealthz: enableHealthz, logger: l}
	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      s.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // chats can take a while
	}
	return s
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	if s.enableHealthz {
		mux.HandleFunc("GET /healthz", s.handleHealthz)
	}

	// providers
	mux.HandleFunc("GET /v1/providers/{id}/auth-methods", s.handleProviderAuthMethods)
	mux.HandleFunc("POST /v1/providers/login", s.handleProviderLogin)
	mux.HandleFunc("POST /v1/providers/logout", s.handleProviderLogout)
	mux.HandleFunc("GET /v1/models", s.handleListModels)

	// HITL policies and MCP servers.
	mux.HandleFunc("GET /v1/policies", s.handleListPolicies)
	mux.HandleFunc("POST /v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("DELETE /v1/policies/{name}", s.handleDeletePolicy)
	mux.HandleFunc("GET /v1/mcp/servers", s.handleListMCPServers)
	mux.HandleFunc("POST /v1/mcp/servers", s.handleAddMCPServer)
	mux.HandleFunc("PUT /v1/mcp/servers/{name}", s.handleEditMCPServer)
	mux.HandleFunc("DELETE /v1/mcp/servers/{name}", s.handleRemoveMCPServer)
	mux.HandleFunc("POST /v1/mcp/servers/{name}/probe", s.handleProbeMCPServer)
	mux.HandleFunc("POST /v1/mcp/servers/{name}/login", s.handleLoginMCPServer)

	// sessions
	mux.HandleFunc("POST /v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /v1/sessions/identities", s.handleGetSessionIdentities)
	mux.HandleFunc("GET /v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /v1/sessions/{id}/identity", s.handleGetSessionIdentity)
	mux.HandleFunc("GET /v1/sessions/{id}/timeline", s.handleGetTimeline)
	mux.HandleFunc("GET /v1/sessions/{id}/report", s.handleGetReport)
	mux.HandleFunc("POST /v1/sessions/{id}/timeline/branch", s.handleBranchTimeline)
	mux.HandleFunc("POST /v1/sessions/{id}/load", s.handleLoadSession)
	mux.HandleFunc("POST /v1/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /v1/sessions/{id}/model", s.handleSetSessionModel)
	mux.HandleFunc("POST /v1/sessions/{id}/policy", s.handleSetSessionPolicy)
	mux.HandleFunc("GET /v1/sessions/{id}/join-stream", s.handleJoinSession)
	mux.HandleFunc("POST /v1/sessions/{id}/chat", s.handleChat)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", s.handleCancelTurn)
	mux.HandleFunc("POST /v1/sessions/{id}/approvals/{approvalID}", s.handleApproval)
	mux.HandleFunc("POST /v1/sessions/{id}/elicitations/{elicitationID}", s.handleResolveElicitation)
	mux.HandleFunc("POST /v1/sessions/{id}/prompts", s.handleRenderPrompt)
	mux.HandleFunc("POST /v1/sessions/{id}/compact", s.handleCompact)
	mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /v1/sessions/{id}/close", s.handleCloseSession)

	return mux
}

func (s *Server) handleCancelTurn(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}

	switch err := s.engine.CancelTurn(id); {
	case errors.Is(err, harnessErr.ErrSessionNotFound):
		s.writeError(w, http.StatusNotFound, err)
	case errors.Is(err, harnessErr.ErrInvalidInput):
		s.writeError(w, http.StatusConflict, err)
	case errors.Is(err, harnessErr.ErrNoTurnToCancel):
		s.writeError(w, http.StatusConflict, err)
	case err != nil:
		s.writeError(w, http.StatusInternalServerError, err)
	default:
		s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	approvalID, err := uuid.Parse(r.PathValue("approvalID"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid approval id"))
		return
	}
	var req contracts.ApprovalConclusion
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.engine.ResolveApproval(sessionID, approvalID, req); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleResolveElicitation(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	elicitationID, err := uuid.Parse(r.PathValue("elicitationID"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid elicitation id"))
		return
	}
	var req glue.ResolveElicitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Action == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("action is required"))
		return
	}
	conclusion := contracts.ElicitationConclusion{ReqID: elicitationID, Action: req.Action, Content: req.Content}
	if err := s.engine.ResolveElicitation(sessionID, elicitationID, conclusion); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleRenderPrompt(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req glue.RenderPromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Server == "" || req.Name == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("server and name are required"))
		return
	}
	messages, err := s.engine.RenderMCPPrompt(id, req.Server, req.Name, req.Arguments)
	if err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, messages)
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.CompactSession(r.Context(), id); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	sess, ok := s.engine.Session(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, harnessErr.ErrSessionNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, sess.Meta())
}

func (s *Server) ListenAndServe() error {
	// Bound first, announced second: a listener that could not be opened — the
	// address is in use, most often — must not be reported as one that is up.
	listener, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return err
	}
	s.logger.Info("agent server listening", "addr", listener.Addr().String())
	return s.httpSrv.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.writeText(w, http.StatusOK, "ok")
}

// ---- providers ----

func (s *Server) handleProviderAuthMethods(w http.ResponseWriter, r *http.Request) {
	methods, err := s.engine.ProviderAuthMethods(contracts.ProviderSlug(r.PathValue("id")))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, methods)
}

func (s *Server) handleProviderLogin(w http.ResponseWriter, r *http.Request) {
	var input glue.LoginProviderInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	err := s.streamLogin(
		w, r,
		func(emit func(any) error) error {
			return s.engine.LoginProvider(r.Context(), input, func(output glue.LoginProviderOutput) error {
				return emit(output)
			})
		},
		func(err error) any {
			return glue.LoginProviderOutput{Status: contracts.ProviderAuthFailed, Error: err.Error()}
		}, "provider", string(input.ProviderID),
	)
	if err != nil {
		s.logger.Debug("provider login stream ended with an error", "provider", input.ProviderID, "error", err)
	}
}

func (s *Server) handleProviderLogout(w http.ResponseWriter, r *http.Request) {
	var input glue.LogoutProviderInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.engine.LogoutProvider(input); err != nil {
		if errors.Is(err, engine.ErrProviderNotLoggedIn) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.engine.ListAllProviderModels())
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.engine.ListMCPServers(r.Context())
	if err != nil {
		s.writeMCPError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, servers)
}

func (s *Server) handleAddMCPServer(w http.ResponseWriter, r *http.Request) {
	input, err := decodeMCPServerInput(w, r)
	if err != nil {
		s.writeMCPDecodeError(w, err)
		return
	}
	view, err := s.engine.AddMCPServer(r.Context(), input)
	if err != nil {
		s.writeMCPError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleEditMCPServer(w http.ResponseWriter, r *http.Request) {
	input, err := decodeMCPServerInput(w, r)
	if err != nil {
		s.writeMCPDecodeError(w, err)
		return
	}
	name := r.PathValue("name")
	if input.Name != name {
		s.writeError(w, http.StatusBadRequest, errors.New("the server name comes from the path and must match the body"))
		return
	}
	view, err := s.engine.EditMCPServer(r.Context(), input)
	if err != nil {
		s.writeMCPError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRemoveMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.RemoveMCPServer(r.Context(), r.PathValue("name")); err != nil {
		s.writeMCPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProbeMCPServer(w http.ResponseWriter, r *http.Request) {
	probe, err := s.engine.ProbeMCPServer(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeMCPError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, probe)
}

// handleLoginMCPServer streams one interactive authorization, shaped exactly
// like handleProviderLogin: the URL the user must open arrives as an event, and
// a failure that happens after the stream opened is a final failed event. The
// target is checked first, so a request that names no such server is answered
// with a status code instead of a failure inside a stream that already said 200.
func (s *Server) handleLoginMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.engine.MCPLoginTarget(name); err != nil {
		s.writeMCPError(w, err)
		return
	}
	err := s.streamLogin(w, r,
		func(emit func(any) error) error {
			return s.engine.LoginMCPServer(r.Context(), glue.MCPLoginInput{Server: name}, func(output glue.MCPLoginOutput) error {
				return emit(output)
			})
		},
		func(err error) any {
			return glue.MCPLoginOutput{Status: contracts.ProviderAuthFailed, Error: err.Error()}
		}, "server", name,
	)
	if err != nil {
		s.logger.Debug("MCP login stream ended with an error", "server", name, "error", err)
	}
}

// streamLogin opens one server-sent-event stream, runs one interactive login on
// it and, if the login fails after the stream opened, writes its last event as
// the failure the client reads. The two login endpoints differ only in what they
// run and in the shape of that event. The error it returns is for the log: the
// client has already been answered.
func (s *Server) streamLogin(
	w http.ResponseWriter,
	r *http.Request,
	run func(emit func(any) error) error,
	failed func(error) any,
	subject, name string,
) error {
	if _, ok := w.(http.Flusher); !ok {
		s.writeError(w, http.StatusBadRequest, errors.New("streaming not supported"))
		return nil
	}
	w.Header().Set("Content-Type", contentTypeEventStream)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	s.flush(w)

	emit := func(output any) error {
		if err := s.writeSSE(w, output); err != nil {
			return err
		}
		s.flush(w)
		return nil
	}
	err := run(emit)
	if err == nil || r.Context().Err() != nil {
		// The client is gone: there is nobody left to tell.
		return nil
	}
	if writeErr := s.writeSSE(w, failed(err)); writeErr != nil {
		s.logger.Debug("login stream error event failed", subject, name, "error", writeErr)
	}
	s.flush(w)
	return err
}

// writeMCPError maps the engine's MCP errors onto status codes: a mistake in
// the request is the caller's, a missing server and a duplicate name are not,
// and anything else is this server failing on its own storage.
func (s *Server) writeMCPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrMCPNotFound):
		s.writeError(w, http.StatusNotFound, err)
	case errors.Is(err, engine.ErrMCPExists):
		s.writeError(w, http.StatusConflict, err)
	case errors.Is(err, engine.ErrMCPOperation):
		// Another registry change or an interactive authorization holds the
		// registry: the request is valid but cannot run now.
		s.writeError(w, http.StatusConflict, err)
	case errors.Is(err, engine.ErrMCPInvalid):
		s.writeError(w, http.StatusBadRequest, err)
	default:
		s.writeError(w, http.StatusInternalServerError, err)
	}
}

// maxMCPServerBodyBytes bounds a server management body: one tools.json entry is
// small even with a token and a handful of headers.
const maxMCPServerBodyBytes = 64 << 10

// writeMCPDecodeError answers a management body that could not be read: over the
// limit is 413, anything else is 400.
func (s *Server) writeMCPDecodeError(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds the %d KB limit", maxMCPServerBodyBytes>>10))
		return
	}
	s.writeError(w, http.StatusBadRequest, err)
}

// decodeMCPServerInput reads a bounded management body. The handler answers its
// two failures differently, so the error is returned rather than written here.
func decodeMCPServerInput(w http.ResponseWriter, r *http.Request) (glue.MCPServerInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMCPServerBodyBytes)
	var input glue.MCPServerInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return glue.MCPServerInput{}, err
	}
	return input, nil
}

// ---- sessions ----

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req glue.CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}

	sess, err := s.engine.CreateSession(r.Context(), engine.CreateSessionOptions{
		Provider: req.Provider,
		Model:    req.Model,
		Cwd:      req.Cwd,
	})
	if err != nil {
		if errors.Is(err, harnessErr.ErrEngineShuttingDown) {
			s.writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, s.sessionOutput(sess))
}

func (s *Server) sessionOutput(sess *session.InfaiAgentSession) glue.SessionOutput {
	return glue.SessionOutput{
		SessionMeta:       sess.Meta(),
		ContextWindow:     sess.CurrentSessionModelContextWindow(),
		Thinking:          sess.CurrentThinkingPattern(),
		AvailableThinking: sess.AvailableThinkingPatterns(),
		InputModalities:   sess.SupportedInputModalities(),
		OutputModalities:  sess.SupportedOutputModalities(),
		Tools:             sess.AvailableTools(),
		Skills:            sess.AvailableSkills(),
		ActivePolicy:      sess.ActivePolicyName(),
		AvailablePolicies: s.engine.PolicyNames(),
		MCP:               sess.MCPStatus(),
	}
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.engine.ListPolicies())
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req glue.CreateHITLPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Policy == nil {
		s.writeError(w, http.StatusBadRequest, errors.New("policy is required"))
		return
	}
	if err := s.engine.CreatePolicy(req.Name, req.Policy); err != nil {
		s.writePolicyError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, s.engine.ListPolicies())
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeletePolicy(r.PathValue("name")); err != nil {
		s.writePolicyError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.engine.ListPolicies())
}

func (s *Server) writePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrPolicyNotFound):
		s.writeError(w, http.StatusNotFound, err)
	case errors.Is(err, engine.ErrPolicyExists):
		s.writeError(w, http.StatusConflict, err)
	default:
		s.writeError(w, http.StatusBadRequest, err)
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.engine.ListSessions())
}

// handleGetSessionIdentities names the sessions the ids belong to, for a client
// holding ids it never opened. It is the plural of the identity route above, and
// ids that name nothing are left out of the answer.
func (s *Server) handleGetSessionIdentities(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()["ids"]
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid session id %q", value))
			return
		}
		ids = append(ids, id)
	}
	s.writeJSON(w, http.StatusOK, s.engine.GetSessionIdentities(ids))
}

func (s *Server) handleGetSessionIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	name, agentKind, err := s.engine.GetSessionIdentity(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, glue.SessionIdentityResponse{Name: name, AgentKind: agentKind})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	meta, records, err := s.engine.GetSessionRecords(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, glue.SessionDetailResponse{Meta: meta, Records: records})
}

func (s *Server) handleGetTimeline(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	meta, events, head, err := s.engine.GetTimeline(id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err)
		return
	}
	response := glue.TimelineResponse{Meta: meta, Head: head, Events: make([]glue.TimelineEventResponse, 0, len(events))}
	for _, event := range events {
		response.Events = append(response.Events, glue.TimelineEventResponse{ID: event.ID, ParentID: event.ParentID, BranchFrom: event.BranchFrom, Kind: event.Kind, BlobHash: event.BlobHash, Preview: event.Preview, Record: event.Record})
	}
	s.writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleGetReport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	page, err := s.engine.GetReport(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, harnessErr.ErrSessionNotIdle) {
			s.writeError(w, http.StatusConflict, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

func (s *Server) handleBranchTimeline(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req glue.BranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == uuid.Nil {
		s.writeError(w, http.StatusBadRequest, errors.New("event_id is required"))
		return
	}
	checklist, err := s.engine.SelectBranch(id, req.EventID)
	if err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"event_id": req.EventID, "selected": true, "task_checklist": checklist})
}

func (s *Server) handleLoadSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	sess, err := s.engine.LoadSession(id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.sessionOutput(sess))
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req glue.RenameSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	meta, err := s.engine.RenameSession(id, req.Name)
	if err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleSetSessionModel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req glue.SetSessionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Provider == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	if req.Model == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("model is required"))
		return
	}

	sess, err := s.engine.SetSessionModel(id, req.Provider, req.Model)
	if err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.sessionOutput(sess))
}

func (s *Server) handleSetSessionPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req glue.SetHITLPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Policy == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("policy is required"))
		return
	}

	sess, err := s.engine.SetSessionPolicy(id, req.Policy)
	if err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.sessionOutput(sess))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	var req glue.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("chat request body exceeds the %d MB limit", maxChatBodyBytes>>20))
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	input := contracts.UserInput{Text: req.Prompt, Images: req.Images}
	if input.Empty() {
		s.writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}

	err = s.engine.Chat(r.Context(), id, input, contracts.ChatOptions{Thinking: req.Thinking})
	switch {
	case errors.Is(err, harnessErr.ErrSessionNotFound):
		s.writeError(w, http.StatusNotFound, err)
	case errors.Is(err, harnessErr.ErrEngineShuttingDown):
		s.writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, harnessErr.ErrInvalidInput):
		s.writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeError(w, http.StatusConflict, err)
	default:
		s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (s *Server) handleJoinSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		s.writeError(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}

	view, events, unsubscribe, err := s.engine.SubscribeEvents(id)
	if err != nil {
		switch {
		case errors.Is(err, harnessErr.ErrSessionNotFound):
			s.writeError(w, http.StatusNotFound, err)
		case errors.Is(err, harnessErr.ErrEngineShuttingDown):
			s.writeError(w, http.StatusServiceUnavailable, err)
		case errors.Is(err, harnessErr.ErrTooManyClients):
			s.writeError(w, http.StatusConflict, err)
		default:
			s.writeError(w, http.StatusConflict, err)
		}
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := s.writeSSE(w, view); err != nil {
		return
	}
	s.flush(w)

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := s.writeSSE(w, event); err != nil {
				return
			}
			s.flush(w)
			if event.Kind == contracts.EventSubscriberGap {
				return
			}
		}
	}
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.DeleteSession(id); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.CloseSession(id); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, harnessErr.ErrInvalidInput) {
			s.writeError(w, http.StatusConflict, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("write response", "status", code, "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, code int, err error) {
	s.writeJSON(w, code, glue.ErrorResponse{Error: err.Error()})
}

// writeText answers with a plain-text body. Like writeJSON, a failed write is
// logged rather than acted on: the status is already sent, and the browser that
// asked may simply have gone.
func (s *Server) writeText(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	if _, err := io.WriteString(w, message); err != nil {
		s.logger.Error("write response", "status", code, "error", err)
	}
}

// writeSSE emits one Server-Sent Event carrying a JSON payload. Returns the
// error so the caller can react to a client that went away mid-stream.
func (s *Server) writeSSE(w http.ResponseWriter, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal sse event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	return nil
}

func (s *Server) flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
