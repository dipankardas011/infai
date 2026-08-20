package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/engine"
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
	mux.HandleFunc("POST /v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("POST /v1/sessions/{id}/chat", s.handleChat)
	mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleDeleteSession)

	s.httpSrv = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // chats can take a while
	}
	return s
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

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.engine.CreateSession(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": sess.ID()})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.ListSessions())
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}

	res, err := s.engine.Chat(r.Context(), id, req.Prompt)
	if errors.Is(err, engine.ErrSessionNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	body := map[string]any{
		"session_id": res.SessionID,
		"status":     res.Status.String(),
		"reply":      res.Reply,
	}
	if res.ReasoningContent != "" {
		body["reasoning_content"] = res.ReasoningContent
	}
	if res.Pending != nil {
		body["pending"] = res.Pending
	}
	if res.Usage != nil {
		body["usage"] = res.Usage
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid session id"))
		return
	}
	if err := s.engine.CloseSession(id); err != nil {
		if errors.Is(err, engine.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}
