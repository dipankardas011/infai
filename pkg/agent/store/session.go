package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// SessionMeta is the runtime session metadata. ContextWindow and TurnCount are
// intentionally not persisted; they are runtime/display values, not session
// identity.
type SessionMeta struct {
	ID            uuid.UUID `json:"id"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Cwd           string    `json:"cwd,omitempty"`
	ContextWindow int       `json:"context_window,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TurnCount     int       `json:"-"`
}

type sessionFile struct {
	ID           uuid.UUID    `json:"id"`
	Cwd          string       `json:"cwd,omitempty"`
	CurrentModel currentModel `json:"current_model"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// this helps when we resume we can use this to get the client connection up.
type currentModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func newSessionFile(meta SessionMeta) sessionFile {
	return sessionFile{
		ID:  meta.ID,
		Cwd: meta.Cwd,
		CurrentModel: currentModel{
			Provider: meta.Provider,
			Model:    meta.Model,
		},
		CreatedAt: meta.CreatedAt,
		UpdatedAt: meta.UpdatedAt,
	}
}

func (f sessionFile) meta() SessionMeta {
	return SessionMeta{
		ID:        f.ID,
		Provider:  f.CurrentModel.Provider,
		Model:     f.CurrentModel.Model,
		Cwd:       f.Cwd,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

// RecordKind identifies the durable event type written to a session timeline.
type RecordKind string

const (
	KindMessage    RecordKind = "message"
	KindDelta      RecordKind = "delta"
	KindToolCall   RecordKind = "tool_call"
	KindToolResult RecordKind = "tool_result"
	KindCompaction RecordKind = "compaction"
)

// ToolCallRecord is what the model requested; ToolResultRecord is what it got
// back. The tool loop is not wired yet — these records exist so the parent
// context history (tool calls + their outputs) survives in the timeline
// once the loop lands.
type ToolCallRecord struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResultRecord struct {
	CallID string `json:"call_id"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

// CompactionRecord marks a point where the active model context was replaced
// by a continuation summary. Earlier timeline events remain untouched.
type CompactionRecord struct {
	Summary string `json:"summary"`
}

// Record is one durable event in a session timeline. Deltas are live-only and
// fan out to sinks; everything else is durable.
type Record struct {
	Kind       RecordKind             `json:"kind"`
	Timestamp  time.Time              `json:"ts"`
	Message    *contracts.ChatMessage `json:"message,omitempty"`
	DeltaKind  contracts.DeltaKind    `json:"delta_kind,omitempty"`
	Text       string                 `json:"text,omitempty"`
	ToolCall   *ToolCallRecord        `json:"tool_call,omitempty"`
	ToolResult *ToolResultRecord      `json:"tool_result,omitempty"`
	Compaction *CompactionRecord      `json:"compaction,omitempty"`
}

// SessionStore reads and writes session timelines under harness/sessions,
// one <uuid> directory per session.
type SessionStore struct {
	root string
	mu   sync.Mutex
}

func OpenSessionStore() (*SessionStore, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	return NewSessionStore(root + "/sessions")
}

// NewSessionStore creates a session store rooted at an explicit directory.
// Used by the harness and by tests that want a sandboxed location.
func NewSessionStore(root string) (*SessionStore, error) {
	if err := EnsureDir(root); err != nil {
		return nil, err
	}
	return &SessionStore{root: root}, nil
}

// SessionStoreDirectory returns the durable event timeline directory for a session.
func (s *SessionStore) SessionStoreDirectory(id uuid.UUID) string {
	return filepath.Join(s.root, id.String())
}

func (s *SessionStore) sessionMetaPath(id uuid.UUID) string {
	return filepath.Join(s.SessionStoreDirectory(id), "session.json")
}

// SaveMeta writes the small session index file used for fast session listing.
func (s *SessionStore) SaveMeta(meta SessionMeta) error {
	if meta.ID == uuid.Nil {
		return fmt.Errorf("store: session metadata has no id")
	}
	if err := EnsureDir(s.SessionStoreDirectory(meta.ID)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(newSessionFile(meta), "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode session metadata: %w", err)
	}
	tmpPath := s.sessionMetaPath(meta.ID) + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("store: write session metadata: %w", err)
	}
	if err := os.Rename(tmpPath, s.sessionMetaPath(meta.ID)); err != nil {
		return fmt.Errorf("store: commit session metadata: %w", err)
	}
	return nil
}

// LoadMeta reads the lightweight session metadata without opening its
// timeline.
func (s *SessionStore) LoadMeta(id uuid.UUID) (SessionMeta, error) {
	data, err := os.ReadFile(s.sessionMetaPath(id))
	if err != nil {
		return SessionMeta{}, fmt.Errorf("store: read session metadata: %w", err)
	}
	var file sessionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return SessionMeta{}, fmt.Errorf("store: decode session metadata: %w", err)
	}
	if file.ID == uuid.Nil {
		return SessionMeta{}, fmt.Errorf("store: session metadata has no id")
	}
	return file.meta(), nil
}

// LoadSessionTimelineClient opens the authoritative event history for a session.
func (s *SessionStore) LoadSessionTimelineClient(id uuid.UUID) (*Timeline, error) {
	return NewTimeline(s.SessionStoreDirectory(id), TimelineOptions{})
}

// List returns every session's lightweight metadata, newest-updated first.
func (s *SessionStore) List() ([]SessionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("store: read timeline sessions: %w", err)
	}
	metas := make([]SessionMeta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, err := uuid.Parse(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, entry.Name(), "session.json"))
		if err != nil {
			continue
		}
		var file sessionFile
		if err := json.Unmarshal(data, &file); err != nil || file.ID == uuid.Nil {
			continue
		}
		metas = append(metas, file.meta())
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	return metas, nil
}

// Delete removes a session timeline from disk. Missing directories are not an
// error.
func (s *SessionStore) Delete(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.SessionStoreDirectory(id)); err != nil {
		return fmt.Errorf("store: delete timeline: %w", err)
	}
	return nil
}
