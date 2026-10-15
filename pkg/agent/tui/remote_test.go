package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// The identity request asks about every id in one call, and reads the answer as
// the engine's own identities: the client and the route have to agree on both.
func TestRemoteClientAsksWhoTheSessionsAre(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/identities" {
			t.Errorf("requested %s %s", r.Method, r.URL.Path)
		}
		asked = r.URL.Query()["ids"]
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"id":%q,"name":"build-worker","agent_kind":"sidecar_loop","status":"completed"}]`, first)
	}))
	defer server.Close()

	client := &RemoteClient{baseURL: server.URL, client: server.Client()}
	identities, err := client.GetSessionIdentities(context.Background(), []uuid.UUID{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{first.String(), second.String()}; !slices.Equal(asked, want) {
		t.Fatalf("asked about %v, want %v", asked, want)
	}
	if len(identities) != 1 {
		t.Fatalf("identities=%+v want the one the engine answered for", identities)
	}
	if got := identities[0]; got.ID != first || got.Name != "build-worker" ||
		got.AgentKind != contracts.SidecarLoopAgent || got.Status != contracts.SessionCompleted {
		t.Fatalf("identity=%+v", got)
	}
}
