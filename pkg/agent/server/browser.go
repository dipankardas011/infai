package server

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"

	"github.com/dipankardas011/infai/pkg/agent/browser"
	"github.com/dipankardas011/infai/pkg/agent/vision"
)

func (s *Server) handleBrowserConnect(w http.ResponseWriter, r *http.Request) {
	bridge := s.engine.Browser()
	if bridge == nil {
		s.writeError(w, http.StatusServiceUnavailable, errors.New("browser bridge is not available"))
		return
	}
	bridge.ServeHTTP(w, r)
}

func (s *Server) handleBrowserStatus(w http.ResponseWriter, _ *http.Request) {
	status := browser.Status{Clients: []browser.Client{}, Sessions: []browser.SessionTab{}}
	if bridge := s.engine.Browser(); bridge != nil {
		status = bridge.Status()
	}
	s.writeJSON(w, http.StatusOK, status)
}

// handleBrowserScreenshot accepts a screenshot the extension uploads out of
// band. The WebSocket reply then carries only an id, so no reply frame has to
// hold megabytes. The pairing token authenticates it, as the socket does.
func (s *Server) handleBrowserScreenshot(w http.ResponseWriter, r *http.Request) {
	bridge := s.engine.Browser()
	if bridge == nil {
		s.writeError(w, http.StatusServiceUnavailable, errors.New("browser bridge is not available"))
		return
	}
	token := r.Header.Get("X-Infai-Browser-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.engine.BrowserToken())) != 1 {
		s.writeError(w, http.StatusUnauthorized, errors.New("invalid browser token"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vision.MaxImageBytes))
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, errors.New("screenshot exceeds the image limit"))
		return
	}
	id, err := bridge.StashImage(body)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"id": id})
}
