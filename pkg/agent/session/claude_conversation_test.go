package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// The record sits beside session.json so a deleted session takes it along, and a
// session that never ran claude-cli simply has no file.
func TestClaudeConversationStoreLivesInTheSessionDirectory(t *testing.T) {
	sessions, err := store.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	conversations := claudeConversationStore(sessions, id)

	if raw, err := conversations.LoadClaudeConversation(); err != nil || raw != nil {
		t.Fatalf("load of an absent record = %q, %v", raw, err)
	}
	if err := conversations.SaveClaudeConversation([]byte(`{"id":"x","sent":3}`)); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(sessions.SessionStoreDirectory(id), claudeConversationFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("record not written to the session directory: %v", err)
	}
	raw, err := conversations.LoadClaudeConversation()
	if err != nil || string(raw) != `{"id":"x","sent":3}` {
		t.Fatalf("round trip = %q, %v", raw, err)
	}
}
