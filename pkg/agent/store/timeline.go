package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Refer: https://github.com/dipankardas011/infai/pull/61#issuecomment-5380172553

const (
	// chunkBytesThreshold is the approximate maximum size of one JSONL chunk
	// before the timeline rotates to the next chunk file. It is 64 MiB.
	chunkBytesThreshold = 64 << 20
	// blobBytesThreshold moves a serialized Record to blob storage when it is
	// at least 1 MiB, keeping large payloads out of the event chunks.
	blobBytesThreshold = 1 << 20
)

var rootEventID = uuid.Nil

type Event struct {
	ID         uuid.UUID
	ParentID   uuid.UUID
	BranchFrom *uuid.UUID
	Kind       RecordKind
	Record     *Record
	BlobHash   string
}

type EventLocation struct {
	Chunk  uint32 `json:"chunk"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type TimelineOptions struct {
	ChunkBytes int64
}

type Timeline struct {
	root       string
	chunksRoot string
	blobsRoot  string
	indexPath  string

	mu          sync.Mutex
	index       map[uuid.UUID]EventLocation
	parents     map[uuid.UUID]uuid.UUID
	head        uuid.UUID
	chunk       uint32
	chunkFile   *os.File
	chunkOffset int64
	chunkBytes  int64
	broken      error
}

type eventDisk struct {
	ID         uuid.UUID  `json:"id"`
	ParentID   uuid.UUID  `json:"parent_id,omitempty"`
	BranchFrom *uuid.UUID `json:"branch_from,omitempty"`
	Kind       RecordKind `json:"kind"`
	Record     *Record    `json:"record,omitempty"`
	BlobHash   string     `json:"blob_hash,omitempty"`
}

type indexDisk struct {
	Type       string         `json:"type"`
	ID         uuid.UUID      `json:"id,omitempty"`
	ParentID   uuid.UUID      `json:"parent_id,omitempty"`
	BranchFrom *uuid.UUID     `json:"branch_from,omitempty"`
	Kind       RecordKind     `json:"kind,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Loc        *EventLocation `json:"location,omitempty"`
	Head       uuid.UUID      `json:"head,omitempty"`
}

func NewTimeline(root string, opts TimelineOptions) (*Timeline, error) {
	if opts.ChunkBytes <= 0 {
		opts.ChunkBytes = chunkBytesThreshold
	}
	for _, dir := range []string{root, filepath.Join(root, "chunks"), filepath.Join(root, "blobs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("timeline: create %s: %w", dir, err)
		}
	}
	t := &Timeline{
		root:       root,
		chunksRoot: filepath.Join(root, "chunks"),
		blobsRoot:  filepath.Join(root, "blobs"),
		indexPath:  filepath.Join(root, "index.jsonl"),
		index:      make(map[uuid.UUID]EventLocation),
		parents:    make(map[uuid.UUID]uuid.UUID),
		chunkBytes: opts.ChunkBytes,
	}
	if err := t.loadIndex(); err != nil {
		return nil, err
	}
	if err := t.reconcileChunks(); err != nil {
		return nil, err
	}
	if err := t.openChunk(); err != nil {
		return nil, err
	}
	return t, nil
}

// AppendToHead appends a normal conversation event after the current HEAD.
func (t *Timeline) AppendToHead(record Record) (Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.appendFromParentLocked(record, t.head, nil)
}

// BranchFromEventID commits a new event from an explicit parent. This is the
// operation used after the user selects a branch point and submits a prompt.
// A nil parent is reserved for the internal root; non-nil IDs create a new
// committed branch.
func (t *Timeline) BranchFromEventID(record Record, parent uuid.UUID) (Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if parent == rootEventID {
		return Event{}, errors.New("timeline: branch of root of the session is not allowed")
	}
	var branchFrom *uuid.UUID
	if t.hasChildLocked(parent) {
		branchFrom = &parent
	}
	return t.appendFromParentLocked(record, parent, branchFrom)
}

// hasChildLocked if it has childreen of a event from whome we are going to branch from if yes
// then we mark it as branch else we remember it as retrugning to main timeline.
func (t *Timeline) hasChildLocked(parent uuid.UUID) bool {
	for child, existingParent := range t.parents {
		if existingParent == parent {
			if _, ok := t.index[child]; !ok {
				continue
			}
			return true
		}
	}
	return false
}

func (t *Timeline) appendFromParentLocked(record Record, parentID uuid.UUID, branchFrom *uuid.UUID) (Event, error) {
	if t.broken != nil {
		return Event{}, fmt.Errorf("timeline: writer requires reopen: %w", t.broken)
	}
	if record.Kind == "" {
		return Event{}, errors.New("timeline: event kind is required")
	}
	if record.Kind == KindDelta {
		return Event{}, errors.New("timeline: delta records are live-only")
	}
	if parentID != rootEventID {
		if _, ok := t.index[parentID]; !ok {
			return Event{}, fmt.Errorf("timeline: parent event %d not found", parentID)
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("timeline: generate event id: %w", err)
	}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return Event{}, fmt.Errorf("timeline: encode record: %w", err)
	}

	disk := eventDisk{ID: id, ParentID: parentID, BranchFrom: branchFrom, Kind: record.Kind}
	if len(encodedRecord) >= blobBytesThreshold {
		hash, err := t.writeBlob(encodedRecord)
		if err != nil {
			return Event{}, err
		}
		disk.BlobHash = hash
	} else {
		disk.Record = &record
	}

	line, err := json.Marshal(disk)
	if err != nil {
		return Event{}, fmt.Errorf("timeline: encode event: %w", err)
	}
	line = append(line, '\n')

	if t.chunkOffset > 0 && t.chunkOffset+int64(len(line)) > t.chunkBytes {
		if err := t.rotateChunk(); err != nil {
			return Event{}, err
		}
	}

	loc := EventLocation{Chunk: t.chunk, Offset: t.chunkOffset, Length: int64(len(line))}
	if _, err := t.chunkFile.Write(line); err != nil {
		t.broken = err
		return Event{}, fmt.Errorf("timeline: append chunk: %w", err)
	}
	if err := t.chunkFile.Sync(); err != nil {
		t.broken = err
		return Event{}, fmt.Errorf("timeline: sync chunk: %w", err)
	}
	t.chunkOffset += int64(len(line))
	if err := t.appendIndex(indexDisk{Type: "event", ID: id, ParentID: parentID, BranchFrom: branchFrom, Kind: record.Kind, Timestamp: record.Timestamp, Loc: &loc}); err != nil {
		t.broken = err
		return Event{}, err
	}
	if err := t.appendIndex(indexDisk{Type: "head", Head: id}); err != nil {
		t.broken = err
		return Event{}, err
	}
	t.index[id] = loc
	t.parents[id] = parentID
	t.head = id
	return Event{ID: id, ParentID: parentID, BranchFrom: branchFrom, Kind: record.Kind, Record: &record, BlobHash: disk.BlobHash}, nil
}

func (t *Timeline) CurrentHeadEventID() uuid.UUID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.head
}

func (t *Timeline) LoadEvent(id uuid.UUID) (Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	loc, ok := t.index[id]
	if !ok {
		return Event{}, fmt.Errorf("timeline: event %d not found", id)
	}
	event, err := t.readAt(id, loc)
	if err != nil {
		return Event{}, err
	}
	if event.Record == nil {
		record, err := t.resolveRecord(event)
		if err != nil {
			return Event{}, err
		}
		event.Record = &record
	}
	return event, nil
}

// LoadFullSessionTimeline returns the complete current session path. Blob
// backed records remain lazy and are not resolved by this call.
func (t *Timeline) LoadFullSessionTimeline() ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.head == rootEventID {
		return []Event{}, nil
	}
	return t.loadFullAncestryLocked(t.head)
}

func (t *Timeline) loadFullAncestryLocked(head uuid.UUID) ([]Event, error) {
	var reverse []Event
	seen := make(map[uuid.UUID]struct{})
	for head != rootEventID {
		if _, ok := seen[head]; ok {
			return nil, fmt.Errorf("timeline: parent cycle at event %d", head)
		}
		seen[head] = struct{}{}
		loc, ok := t.index[head]
		if !ok {
			return nil, fmt.Errorf("timeline: parent event %d not found", head)
		}
		event, err := t.readAt(head, loc)
		if err != nil {
			return nil, err
		}
		reverse = append(reverse, event)
		head = event.ParentID
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, nil
}

// LoadActiveSessionTimeline returns the current session context after the
// latest compaction. Blob backed records remain lazy and are not resolved.
func (t *Timeline) LoadActiveSessionTimeline() ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.head == rootEventID {
		return []Event{}, nil
	}
	return t.loadActiveAncestryLocked(t.head)
}

// LoadActiveSessionTimelineAt loads active context for a temporary branch
// parent. Ordinary session access should use LoadActiveSessionTimeline, which
// reads the timeline's internal HEAD.
func (t *Timeline) LoadActiveSessionTimelineAt(head uuid.UUID) ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if head == rootEventID {
		return nil, errors.New("timeline: nil ancestry head is not allowed")
	}
	return t.loadActiveAncestryLocked(head)
}

func (t *Timeline) loadActiveAncestryLocked(head uuid.UUID) ([]Event, error) {
	var reverse []Event
	seen := make(map[uuid.UUID]struct{})
	for head != rootEventID {
		if _, ok := seen[head]; ok {
			return nil, fmt.Errorf("timeline: parent cycle at event %d", head)
		}
		seen[head] = struct{}{}
		loc, ok := t.index[head]
		if !ok {
			return nil, fmt.Errorf("timeline: parent event %d not found", head)
		}
		event, err := t.readAt(head, loc)
		if err != nil {
			return nil, err
		}
		reverse = append(reverse, event)
		if event.Kind == KindCompaction {
			break
		}
		head = event.ParentID
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, nil
}

func (t *Timeline) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chunkFile == nil {
		return nil
	}
	syncErr := t.chunkFile.Sync()
	closeErr := t.chunkFile.Close()
	t.chunkFile = nil
	return errors.Join(syncErr, closeErr)
}

func (t *Timeline) appendIndex(entry indexDisk) error {
	f, err := os.OpenFile(t.indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("timeline: open index: %w", err)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return errors.Join(err, f.Close())
	}

	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return errors.Join(fmt.Errorf("timeline: append index: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("timeline: sync index: %w", err), f.Close())
	}
	return f.Close()
}

func (t *Timeline) loadIndex() error {
	f, err := os.Open(t.indexPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var entry indexDisk
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			// A torn final index line is recoverable from the chunks.
			continue
		}
		switch entry.Type {
		case "event":
			if entry.Loc == nil {
				continue
			}
			t.index[entry.ID] = *entry.Loc
			t.parents[entry.ID] = entry.ParentID
		case "head":
			t.head = entry.Head
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

// reconcileChunks makes the durable chunks authoritative when an append was
// fsynced before its index lines were. A partial final line is truncated; an
// invalid complete line is treated as corruption.
func (t *Timeline) reconcileChunks() error {
	files, err := t.chunkFiles()
	if err != nil {
		return err
	}
	var missing []indexDisk
	var latest uuid.UUID
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		offset := 0
		for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			complete := line[len(line)-1] == '\n'
			raw := bytes.TrimSuffix(line, []byte{'\n'})
			if len(bytes.TrimSpace(raw)) == 0 {
				offset += len(line)
				continue
			}
			var disk eventDisk
			if err := json.Unmarshal(raw, &disk); err != nil {
				if !complete && offset+len(line) == len(data) {
					if err := os.Truncate(file.path, int64(offset)); err != nil {
						return err
					}
					break
				}
				return fmt.Errorf("timeline: corrupt chunk %s at offset %d: %w", file.path, offset, err)
			}
			loc := EventLocation{Chunk: file.number, Offset: int64(offset), Length: int64(len(line))}
			if current, ok := t.index[disk.ID]; !ok || current != loc {
				var kind RecordKind
				var ts time.Time
				kind = disk.Kind
				if kind == "" && disk.Record != nil {
					kind = disk.Record.Kind
					ts = disk.Record.Timestamp
				}
				missing = append(missing, indexDisk{Type: "event", ID: disk.ID, ParentID: disk.ParentID, BranchFrom: disk.BranchFrom, Kind: kind, Timestamp: ts, Loc: &loc})
				t.index[disk.ID] = loc
				t.parents[disk.ID] = disk.ParentID
			}
			latest = disk.ID
			offset += len(line)
		}
	}
	for _, entry := range missing {
		if err := t.appendIndex(entry); err != nil {
			return err
		}
	}
	if latest != rootEventID && t.head != latest {
		if err := t.appendIndex(indexDisk{Type: "head", Head: latest}); err != nil {
			return err
		}
		t.head = latest
	}
	return nil
}

type chunkFile struct {
	number uint32
	path   string
}

func (t *Timeline) chunkFiles() ([]chunkFile, error) {
	entries, err := filepath.Glob(filepath.Join(t.chunksRoot, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	files := make([]chunkFile, 0, len(entries))
	for _, path := range entries {
		var number uint32
		if _, err := fmt.Sscanf(filepath.Base(path), "%d.jsonl", &number); err != nil {
			return nil, fmt.Errorf("timeline: parse chunk name %s: %w", path, err)
		}
		files = append(files, chunkFile{number: number, path: path})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].number < files[j].number })
	return files, nil
}

func (t *Timeline) openChunk() error {
	files, err := t.chunkFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		t.chunk = 0
	} else {
		t.chunk = files[len(files)-1].number
	}
	path := filepath.Join(t.chunksRoot, fmt.Sprintf("%06d.jsonl", t.chunk))
	t.chunkFile, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := t.chunkFile.Stat()
	if err != nil {
		t.chunkFile.Close()
		return err
	}
	t.chunkOffset = info.Size()
	return nil
}

func (t *Timeline) rotateChunk() error {
	if err := t.chunkFile.Sync(); err != nil {
		return err
	}
	if err := t.chunkFile.Close(); err != nil {
		return err
	}
	t.chunk++
	t.chunkOffset = 0
	path := filepath.Join(t.chunksRoot, fmt.Sprintf("%06d.jsonl", t.chunk))

	var err error
	t.chunkFile, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err == nil {
		err = syncTimelineDir(t.chunksRoot)
	}
	return err
}

func (t *Timeline) readAt(id uuid.UUID, loc EventLocation) (Event, error) {
	f, err := os.Open(filepath.Join(t.chunksRoot, fmt.Sprintf("%06d.jsonl", loc.Chunk)))
	if err != nil {
		return Event{}, err
	}
	defer f.Close()
	if _, err := f.Seek(loc.Offset, io.SeekStart); err != nil {
		return Event{}, err
	}
	data := make([]byte, loc.Length)
	if _, err := io.ReadFull(f, data); err != nil {
		return Event{}, err
	}
	var disk eventDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return Event{}, fmt.Errorf("timeline: decode event %d: %w", id, err)
	}
	if disk.ID != id {
		return Event{}, fmt.Errorf("timeline: index points to event %d, requested %d", disk.ID, id)
	}
	var record *Record
	if disk.Record != nil {
		decoded := *disk.Record
		record = &decoded
	} else if disk.BlobHash == "" {
		return Event{}, fmt.Errorf("timeline: event %d has no record", id)
	}
	kind := disk.Kind
	if kind == "" && disk.Record != nil {
		kind = disk.Record.Kind
	}
	return Event{ID: disk.ID, ParentID: disk.ParentID, BranchFrom: disk.BranchFrom, Kind: kind, Record: record, BlobHash: disk.BlobHash}, nil
}

// ResolveRecord materializes a blob-backed event when its content is needed.
// Ancestry reads intentionally leave blobs as placeholders.
func (t *Timeline) ResolveRecord(event Event) (Record, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resolveRecord(event)
}

func (t *Timeline) resolveRecord(event Event) (Record, error) {
	if event.Record != nil {
		return *event.Record, nil
	}
	if event.BlobHash == "" {
		return Record{}, fmt.Errorf("timeline: event %d has no record", event.ID)
	}
	data, err := os.ReadFile(filepath.Join(t.blobsRoot, event.BlobHash))
	if err != nil {
		return Record{}, fmt.Errorf("timeline: read blob %s: %w", event.BlobHash, err)
	}
	digest := sha256.Sum256(data)
	if actual := hex.EncodeToString(digest[:]); actual != event.BlobHash {
		return Record{}, fmt.Errorf("timeline: blob %s hash mismatch: got %s", event.BlobHash, actual)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("timeline: decode blob %s: %w", event.BlobHash, err)
	}
	return record, nil
}

func (t *Timeline) writeBlob(payload []byte) (string, error) {
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	path := filepath.Join(t.blobsRoot, hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.CreateTemp(t.blobsRoot, ".blob-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(payload); err != nil {
		return "", errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return "", errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := syncTimelineDir(t.blobsRoot); err != nil {
		return "", err
	}
	return hash, nil
}

func syncTimelineDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
