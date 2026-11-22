package server

import (
	"errors"
	"net/http"

	"github.com/dipankardas011/infai/pkg/agent/browser"
)

// handleBrowserConnect is the WebSocket the infai Chrome extension dials.
func (s *Server) handleBrowserConnect(w http.ResponseWriter, r *http.Request) {
	bridge := s.engine.Browser()
	if bridge == nil {
		s.writeError(w, http.StatusServiceUnavailable, errors.New("browser bridge is not available"))
		return
	}
	bridge.ServeHTTP(w, r)
}

func (s *Server) handleBrowserStatus(w http.ResponseWriter, _ *http.Request) {
	status := browser.Status{Sessions: []browser.SessionTab{}}
	if bridge := s.engine.Browser(); bridge != nil {
		status = bridge.Status()
	}
	s.writeJSON(w, http.StatusOK, status)
}
