package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/engine"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/dipankardas011/infai/pkg/agent/glue"
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

func New(l *slog.Logger, e *engine.InfaiAgentEngine, addr string, enableHealthz bool) *Server {
	s := &Server{engine: e, enableHealthz: enableHealthz, logger: l}

	mux := http.NewServeMux()
	if s.enableHealthz {
		mux.HandleFunc("GET /healthz", s.handleHealthz)
	}

	// providers
	mux.HandleFunc("GET /v1/providers/{id}/auth-methods", s.handleProviderAuthMethods)
	mux.HandleFunc("POST /v1/providers/login", s.handleProviderLogin)
	mux.HandleFunc("POST /v1/providers/logout", s.handleProviderLogout)
	mux.HandleFunc("GET /v1/models", s.handleListModels)

	// sessions
	mux.HandleFunc("POST /v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /v1/sessions/{id}/timeline", s.handleGetTimeline)
	mux.HandleFunc("POST /v1/sessions/{id}/timeline/branch", s.handleBranchTimeline)
	mux.HandleFunc("POST /v1/sessions/{id}/load", s.handleLoadSession)
	mux.HandleFunc("POST /v1/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /v1/sessions/{id}/model", s.handleSetSessionModel)
	mux.HandleFunc("GET /v1/sessions/{id}/join-stream", s.handleJoinSession)
	mux.HandleFunc("POST /v1/sessions/{id}/chat", s.handleChat)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", s.handleCancelTurn)
	mux.HandleFunc("POST /v1/sessions/{id}/approvals/{approvalID}", s.handleApproval)
	mux.HandleFunc("POST /v1/sessions/{id}/compact", s.handleCompact)
	mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleDeleteSession)

	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // chats can take a while
	}
	return s
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
	s.logger.Info("agent server listening", "addr", s.httpSrv.Addr)
	return s.httpSrv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
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
	if _, ok := w.(http.Flusher); !ok {
		s.writeError(w, http.StatusBadRequest, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	s.flush(w)

	err := s.engine.LoginProvider(r.Context(), input, func(output glue.LoginProviderOutput) error {
		if err := s.writeSSE(w, output); err != nil {
			return err
		}
		s.flush(w)
		return nil
	})
	if err == nil || r.Context().Err() != nil {
		return
	}
	if writeErr := s.writeSSE(w, glue.LoginProviderOutput{Status: contracts.ProviderAuthFailed, Error: err.Error()}); writeErr != nil {
		s.logger.Debug("provider login stream error event failed", "provider", input.ProviderID, "error", writeErr)
	}
	s.flush(w)
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
	s.writeJSON(w, http.StatusCreated, glue.SessionOutput{
		SessionMeta:       sess.Meta(),
		ContextWindow:     sess.CurrentSessionModelContextWindow(),
		Thinking:          sess.CurrentThinkingPattern(),
		AvailableThinking: sess.AvailableThinkingPatterns(),
		Modalities:        sess.SupportedModalities(),
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.engine.ListSessions())
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
	s.writeJSON(w, http.StatusOK, glue.SessionOutput{
		SessionMeta:       sess.Meta(),
		ContextWindow:     sess.CurrentSessionModelContextWindow(),
		Thinking:          sess.CurrentThinkingPattern(),
		AvailableThinking: sess.AvailableThinkingPatterns(),
		Modalities:        sess.SupportedModalities(),
	})
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
	s.writeJSON(w, http.StatusOK, glue.SessionOutput{
		SessionMeta:       sess.Meta(),
		ContextWindow:     sess.CurrentSessionModelContextWindow(),
		Thinking:          sess.CurrentThinkingPattern(),
		AvailableThinking: sess.AvailableThinkingPatterns(),
		Modalities:        sess.SupportedModalities(),
	})
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
	if err := s.engine.CloseSession(id); err != nil {
		if errors.Is(err, harnessErr.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
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
