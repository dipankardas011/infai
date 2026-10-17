package server

import (
	"net/http/httptest"
	"testing"
)

// A handler that is never registered still compiles, and the request falls
// through to whichever pattern does match — which is how a client ends up
// talking to the wrong endpoint and reading a 400 as an answer. The paths the
// client depends on are asserted against the routes the server actually builds,
// with no engine behind them.
func TestRoutesMatchThePathsTheClientCalls(t *testing.T) {
	mux := (&Server{enableHealthz: true}).routes()
	for _, want := range []struct {
		method string
		path   string
		match  string
	}{
		{"GET", "/healthz", "GET /healthz"},
		{"GET", "/v1/sessions", "GET /v1/sessions"},
		{"GET", "/v1/sessions/identities?ids=01a114d7-0234-7b1a-b629-c877f0e15a24", "GET /v1/sessions/identities"},
		{"GET", "/v1/sessions/01a114d7-0234-7b1a-b629-c877f0e15a24", "GET /v1/sessions/{id}"},
		{"GET", "/v1/sessions/01a114d7-0234-7b1a-b629-c877f0e15a24/identity", "GET /v1/sessions/{id}/identity"},
		{"GET", "/v1/sessions/01a114d7-0234-7b1a-b629-c877f0e15a24/timeline", "GET /v1/sessions/{id}/timeline"},
		{"GET", "/v1/sessions/01a114d7-0234-7b1a-b629-c877f0e15a24/report", "GET /v1/sessions/{id}/report"},
		{"POST", "/v1/sessions/01a114d7-0234-7b1a-b629-c877f0e15a24/close", "POST /v1/sessions/{id}/close"},
	} {
		_, pattern := mux.Handler(httptest.NewRequest(want.method, want.path, nil))
		if pattern == "" {
			t.Errorf("%s %s matches nothing", want.method, want.path)
			continue
		}
		// The engine is nil here, so a route that matched a wildcard where a
		// literal was meant is exactly the failure this proves: the pattern, not
		// the answer, is what is being read.
		if pattern != want.match {
			t.Errorf("%s %s matched %q, want %q", want.method, want.path, pattern, want.match)
		}
	}
}
