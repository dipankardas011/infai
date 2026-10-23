package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dipankardas011/infai/pkg/agent/models"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// claudeConversationFileName holds the claude-cli adaptor's bookkeeping: which
// CLI session the conversation lives in, how much history it already has, and
// whether it can be resumed. It sits beside session.json so deleting a session
// takes it too.
const claudeConversationFileName = "claude-cli.json"

// claudeConversationFile is the adapter's store on disk. Writing it is what lets
// a rebuilt adaptor — a model switch, a thinking-level change, a restart —
// resume the CLI conversation instead of starting a second one.
type claudeConversationFile struct {
	path string
}

func (f claudeConversationFile) LoadClaudeConversation() ([]byte, error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func (f claudeConversationFile) SaveClaudeConversation(raw []byte) error {
	if err := store.EnsureDir(filepath.Dir(f.path)); err != nil {
		return err
	}
	return os.WriteFile(f.path, raw, 0o600)
}

func (s *InfaiAgentSession) claudeConversationStore() models.ClaudeConversationStore {
	return claudeConversationStore(s.store, s.meta.ID)
}

func claudeConversationStore(sessions *store.SessionStore, id uuid.UUID) models.ClaudeConversationStore {
	return claudeConversationFile{path: filepath.Join(sessions.SessionStoreDirectory(id), claudeConversationFileName)}
}
