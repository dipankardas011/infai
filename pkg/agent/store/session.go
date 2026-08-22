package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/google/uuid"
)

// SessionMeta is the header of a session transcript: which provider/model it
// runs, where (cwd), and how many turns it has had. The first JSONL line is
// the initial meta; updates are appended as later meta records, so readers use
// the last one.
type SessionMeta struct {
	ID            uuid.UUID `json:"id"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Cwd           string    `json:"cwd,omitempty"`
	ContextWindow int       `json:"context_window"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TurnCount     int       `json:"turn_count"`
}

// RecordKind identifies the durable event type written to a session timeline.
type RecordKind string

const (
	KindMeta       RecordKind = "meta"
	KindMessage    RecordKind = "message"
	KindDelta      RecordKind = "delta"
	KindToolCall   RecordKind = "tool_call"
	KindToolResult RecordKind = "tool_result"
	KindCompaction RecordKind = "compaction"
)

// ToolCallRecord is what the model requested; ToolResultRecord is what it got
// back. The tool loop is not wired yet — these records exist so the parent
// context history (tool calls + their outputs) survives into the transcript
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

// Record is one JSON line in a session transcript. Deltas are live-only (they
// fan out to sinks but are never persisted); everything else is the durable
// transcript.
type Record struct {
	Kind       RecordKind             `json:"kind"`
	Timestamp  time.Time              `json:"ts"`
	Meta       *SessionMeta           `json:"meta,omitempty"`
	Message    *contracts.ChatMessage `json:"message,omitempty"`
	DeltaKind  string                 `json:"delta_kind,omitempty"`
	Text       string                 `json:"text,omitempty"`
	ToolCall   *ToolCallRecord        `json:"tool_call,omitempty"`
	ToolResult *ToolResultRecord      `json:"tool_result,omitempty"`
	Compaction *CompactionRecord      `json:"compaction,omitempty"`
}

// SessionStore reads and writes session transcripts under harness/sessions,
// one <uuid>.jsonl file per session.
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

// Path returns the transcript file for a session.
func (s *SessionStore) Path(id uuid.UUID) string {
	return filepath.Join(s.root, id.String()+".jsonl")
}

// List returns every saved session's meta, newest-updated first.
func (s *SessionStore) List() ([]SessionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	matches, err := filepath.Glob(filepath.Join(s.root, "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("store: glob sessions: %w", err)
	}

	metas := make([]SessionMeta, 0, len(matches))
	for _, m := range matches {
		meta, err := s.readMetaLocked(m)
		if err != nil {
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	return metas, nil
}

func (s *SessionStore) readMetaLocked(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var meta SessionMeta
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Kind == KindMeta && rec.Meta != nil {
			meta = *rec.Meta
		}
	}
	if err := sc.Err(); err != nil {
		return SessionMeta{}, fmt.Errorf("store: scan session: %w", err)
	}
	if meta.ID == uuid.Nil {
		return SessionMeta{}, fmt.Errorf("store: no meta record in %s", path)
	}
	return meta, nil
}

// Load reads a session transcript back into its meta and full record list.
func (s *SessionStore) Load(id uuid.UUID) (SessionMeta, []Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.Path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return SessionMeta{}, nil, ErrNotFound
		}
		return SessionMeta{}, nil, fmt.Errorf("store: open session: %w", err)
	}
	defer f.Close()

	var meta SessionMeta
	records := []Record{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return SessionMeta{}, nil, fmt.Errorf("store: session %s corrupt record on line %d: %w", id, lineNo, err)
		}
		if rec.Kind == KindMeta && rec.Meta != nil {
			meta = *rec.Meta // last meta wins: it carries the accumulated state
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return SessionMeta{}, nil, fmt.Errorf("store: scan session: %w", err)
	}
	if meta.ID == uuid.Nil {
		return SessionMeta{}, nil, fmt.Errorf("store: session %s has no meta record", id)
	}
	return meta, records, nil
}

// Delete removes a session transcript from disk. Missing files are not an
// error.
func (s *SessionStore) Delete(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.Path(id))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// OpenRecorder opens (creating if needed) the transcript file for meta and
// returns a Recorder bound to it. The meta record is written only when the
// file was newly created, so resuming a saved session does not duplicate it.
func (s *SessionStore) OpenRecorder(meta SessionMeta) (*Recorder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.Path(meta.ID)
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		return newRecorderAppend(path)
	}

	r, err := newRecorderAppend(path)
	if err != nil {
		return nil, err
	}
	if err := r.appendLocked(Record{
		Kind:      KindMeta,
		Timestamp: meta.UpdatedAt,
		Meta:      &meta,
	}); err != nil {
		r.Close()
		return nil, err
	}
	if err := r.Sync(); err != nil {
		r.Close()
		return nil, err
	}
	if err := syncDir(path); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Reconstruct rebuilds a chat history from a record list (message records in
// order).
func (s *SessionStore) Reconstruct(records []Record) []contracts.ChatMessage {
	var msgs []contracts.ChatMessage
	for _, rec := range records {
		if rec.Kind == KindMessage && rec.Message != nil {
			msgs = append(msgs, *rec.Message)
		}
	}
	return msgs
}

// Append persists messages (and their tool call / result records) to a
// session file. This is the batch persistence path; the live path is the
// Recorder. Implements contracts.SessionMemory.
func (s *SessionStore) Append(sessId uuid.UUID, messages ...contracts.ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, err := newRecorderAppend(s.Path(sessId))
	if err != nil {
		return err
	}
	defer r.Close()

	for _, m := range messages {
		if err := r.appendLocked(Record{Kind: KindMessage, Timestamp: time.Now().UTC(), Message: &m}); err != nil {
			return err
		}
		for _, tc := range m.ToolCalls {
			if err := r.appendLocked(Record{
				Kind:      KindToolCall,
				Timestamp: time.Now().UTC(),
				ToolCall: &ToolCallRecord{
					ID:        tc.ID,
					Type:      tc.Type,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}); err != nil {
				return err
			}
		}
		if m.Role == "tool" {
			if err := r.appendLocked(Record{
				Kind:      KindToolResult,
				Timestamp: time.Now().UTC(),
				ToolResult: &ToolResultRecord{
					CallID: m.ToolCallID,
					Status: "success",
					Output: m.Text(),
				},
			}); err != nil {
				return err
			}
		}
	}
	return r.Sync()
}

// SessionMemory-compatible string-keyed wrappers.

func (s *SessionStore) LoadMessages(sessId string) ([]contracts.ChatMessage, error) {
	id, err := uuid.Parse(sessId)
	if err != nil {
		return nil, fmt.Errorf("store: invalid session id: %w", err)
	}
	_, records, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	return s.Reconstruct(records), nil
}

func (s *SessionStore) AppendMessages(sessId string, messages ...contracts.ChatMessage) error {
	id, err := uuid.Parse(sessId)
	if err != nil {
		return fmt.Errorf("store: invalid session id: %w", err)
	}
	return s.Append(id, messages...)
}

func (s *SessionStore) DeleteSession(sessId string) error {
	id, err := uuid.Parse(sessId)
	if err != nil {
		return fmt.Errorf("store: invalid session id: %w", err)
	}
	return s.Delete(id)
}

// syncDir fsyncs the directory containing path so that a newly created file's
// directory entry survives a crash. Without it, a fresh transcript file can
// vanish on power loss even though its contents were synced.
func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("store: open session dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("store: sync session dir: %w", err)
	}
	return nil
}
