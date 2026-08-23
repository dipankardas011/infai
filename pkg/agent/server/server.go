package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/engine"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

type Server struct {
	engine        *engine.InfaiAgentEngine
	httpSrv       *http.Server
	enableHealthz bool
	logger        *slog.Logger
}

func New(l *slog.Logger, e *engine.InfaiAgentEngine, addr string, enableHealthz bool) *Server {
	s := &Server{engine: e, enableHealthz: enableHealthz, logger: l}

	mux := http.NewServeMux()
	if s.enableHealthz {
		mux.HandleFunc("GET /healthz", s.handleHealthz)
	}

	// providers (read-only; the registry is configured via models.json)
	mux.HandleFunc("GET /v1/providers", s.handleListProviders)

	// sessions
	mux.HandleFunc("POST /v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /v1/sessions/{id}/timeline", s.handleGetTimeline)
	mux.HandleFunc("POST /v1/sessions/{id}/timeline/branch", s.handleBranchTimeline)
	mux.HandleFunc("POST /v1/sessions/{id}/load", s.handleLoadSession)
	mux.HandleFunc("POST /v1/sessions/{id}/model", s.handleSetSessionModel)
	mux.HandleFunc("POST /v1/sessions/{id}/chat", s.handleChat)
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

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.CompactSession(r.Context(), id); err != nil {
		if errors.Is(err, engine.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	sess, ok := s.engine.Session(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, engine.ErrSessionNotFound)
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

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.engine.ListProviders())
}

// ---- sessions ----

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
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
		if errors.Is(err, engine.ErrEngineShuttingDown) {
			s.writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, sess.Meta())
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
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, SessionDetailResponse{Meta: meta, Records: records})
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
	response := TimelineResponse{Meta: meta, Head: head, Events: make([]TimelineEventResponse, 0, len(events))}
	for _, event := range events {
		response.Events = append(response.Events, TimelineEventResponse{ID: event.ID, ParentID: event.ParentID, BranchFrom: event.BranchFrom, Kind: event.Kind, BlobHash: event.BlobHash, Record: event.Record})
	}
	s.writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleBranchTimeline(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req BranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventID == uuid.Nil {
		s.writeError(w, http.StatusBadRequest, errors.New("event_id is required"))
		return
	}
	if err := s.engine.SelectBranch(id, req.EventID); err != nil {
		if errors.Is(err, engine.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"event_id": req.EventID, "selected": true})
}

func (s *Server) handleLoadSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	sess, err := s.engine.LoadSession(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, engine.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sess.Meta())
}

func (s *Server) handleSetSessionModel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	var req SetSessionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Provider == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("provider is required"))
		return
	}
	sess, err := s.engine.SetSessionModel(id, req.Provider, req.Model)
	if err != nil {
		if errors.Is(err, engine.ErrSessionNotFound) {
			s.writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sess.Meta())
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}

	sess, ok := s.engine.Session(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, engine.ErrSessionNotFound)
		return
	}

	stream := r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")

	var opts engine.ChatOptions
	if stream {
		if _, ok := w.(http.Flusher); !ok {
			s.writeError(w, http.StatusBadRequest, errors.New("streaming not supported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// The session's recorder is the multi-writer: it feeds this SSE sink
		// for the live stream; durable records are written to the timeline.
		remove := sess.EventHub().Subscribe(func(rec store.Record) error {
			if rec.Kind != store.KindDelta {
				return nil
			}
			if err := s.writeSSE(w, ChatDeltaEvent{Kind: string(rec.DeltaKind), Delta: rec.Text}); err != nil {
				s.logger.Debug("stream write failed", "session_id", id, "error", err)
				return err
			}
			s.flush(w)
			return nil
		})
		defer remove()
	}

	res, err := s.engine.Chat(r.Context(), id, req.Prompt, opts)
	if errors.Is(err, engine.ErrSessionNotFound) {
		s.writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		if stream {
			if werr := s.writeSSE(w, ChatErrorEvent{Error: err.Error()}); werr != nil {
				s.logger.Debug("stream error event failed", "session_id", id, "error", werr)
			}
			s.flush(w)
			return
		}
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}

	meta := sess.Meta()
	if stream {
		done := ChatDoneEvent{
			Done:             true,
			SessionID:        res.SessionID,
			Status:           res.Status.String(),
			Reply:            res.Reply,
			ReasoningContent: res.ReasoningContent,
			Model:            meta.Model,
			ContextWindow:    meta.ContextWindow,
			Pending:          res.Pending,
			Usage:            res.Usage,
		}
		if werr := s.writeSSE(w, done); werr != nil {
			s.logger.Debug("stream done event failed", "session_id", id, "error", werr)
		}
		s.flush(w)
		return
	}

	body := ChatResponse{
		SessionID:        res.SessionID,
		Status:           res.Status.String(),
		Reply:            res.Reply,
		Model:            meta.Model,
		ContextWindow:    meta.ContextWindow,
		ReasoningContent: res.ReasoningContent,
		Pending:          res.Pending,
		Usage:            res.Usage,
	}
	s.writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.CloseSession(id); err != nil {
		if errors.Is(err, engine.ErrSessionNotFound) {
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
	s.writeJSON(w, code, ErrorResponse{Error: err.Error()})
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
