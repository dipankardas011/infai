package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// Session files written before the agent kind was recorded have no
// "agent_kind" key. They must read back as interactive, never as an unknown
// kind, or a resume starts a session no branch of the agent loop handles.
func TestSessionMetaDefaultsMissingAgentKind(t *testing.T) {
	sessions, err := NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	id := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	dir := sessions.SessionStoreDirectory(id)
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	legacy := `{
  "id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
  "current_model": {"provider": "openai", "model": "gpt-5"},
  "created_at": "2026-09-01T00:00:00Z",
  "updated_at": "2026-09-01T00:00:00Z"
}
`
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write session.json: %v", err)
	}

	meta, err := sessions.LoadMeta(id)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.AgentKind != contracts.InteractiveAgent {
		t.Fatalf("LoadMeta AgentKind = %q, want %q", meta.AgentKind, contracts.InteractiveAgent)
	}

	metas, err := sessions.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("List returned %d sessions, want 1", len(metas))
	}
	if metas[0].AgentKind != contracts.InteractiveAgent {
		t.Fatalf("List AgentKind = %q, want %q", metas[0].AgentKind, contracts.InteractiveAgent)
	}
}
