package store

import (
	"bufio"
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

type EventID uint64

const RootEventID EventID = 0

type Event struct {
	ID       EventID
	ParentID EventID
	Record   *Record
	BlobHash string
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
	index       map[EventID]EventLocation
	parents     map[EventID]EventID
	head        EventID
	next        EventID
	chunk       uint32
	chunkFile   *os.File
	chunkOffset int64
	chunkBytes  int64
}

type eventDisk struct {
	ID       EventID `json:"id"`
	ParentID EventID `json:"parent_id,omitempty"`
	Record   *Record `json:"record,omitempty"`
	BlobHash string  `json:"blob_hash,omitempty"`
}

type indexDisk struct {
	Type   string         `json:"type"`
	ID     EventID        `json:"id,omitempty"`
	Parent EventID        `json:"parent_id,omitempty"`
	Loc    *EventLocation `json:"location,omitempty"`
	Head   EventID        `json:"head,omitempty"`
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
		index:      make(map[EventID]EventLocation),
		parents:    make(map[EventID]EventID),
		next:       1,
		chunkBytes: opts.ChunkBytes,
	}
	if err := t.loadIndex(); err != nil {
		return nil, err
	}
	if err := t.openChunk(); err != nil {
		return nil, err
	}
	return t, nil
}

// AppendToHead appends a normal conversation event after the current HEAD.
func (t *Timeline) AppendToHead(record Record) (Event, error) {
	return t.AppendFromParent(record, t.CurrentHeadEventID())
}

// AppendFromParent commits a new event from an explicit parent. This is the
// operation used after the user selects a branch point and submits a prompt.
// RootEventID creates a root event; non-root IDs create a new committed branch.
func (t *Timeline) AppendFromParent(record Record, parent EventID) (Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if record.Kind == "" {
		return Event{}, errors.New("timeline: event kind is required")
	}
	id := t.next
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return Event{}, fmt.Errorf("timeline: encode record: %w", err)
	}

	disk := eventDisk{ID: id, ParentID: parent}
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
		return Event{}, fmt.Errorf("timeline: append chunk: %w", err)
	}
	if err := t.chunkFile.Sync(); err != nil {
		return Event{}, fmt.Errorf("timeline: sync chunk: %w", err)
	}
	if err := t.appendIndex(indexDisk{Type: "event", ID: id, Parent: parent, Loc: &loc}); err != nil {
		return Event{}, err
	}
	if err := t.appendIndex(indexDisk{Type: "head", Head: id}); err != nil {
		return Event{}, err
	}
	t.index[id] = loc
	t.parents[id] = parent
	t.head = id
	t.next++
	t.chunkOffset += int64(len(line))
	return Event{ID: id, ParentID: parent, Record: &record, BlobHash: disk.BlobHash}, nil
}

func (t *Timeline) CurrentHeadEventID() EventID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.head
}

func (t *Timeline) LoadEvent(id EventID) (Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	loc, ok := t.index[id]
	if !ok {
		return Event{}, fmt.Errorf("timeline: event %d not found", id)
	}
	return t.readAt(id, loc)
}

// Path returns the active ancestry in chronological order. The traversal is
// iterative to avoid stack growth on long sessions.
func (t *Timeline) LoadFullAncestry(head EventID) ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if head == 0 {
		head = t.head
	}
	var reverse []Event
	seen := make(map[EventID]struct{})
	for head != 0 {
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

// LoadActiveAncestry returns only the selected ancestry needed for the active
// model context, stopping at the latest compaction marker. Full history remains
// available through LoadFullAncestry.
func (t *Timeline) LoadActiveAncestry(head EventID) ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if head == 0 {
		head = t.head
	}

	var reverse []Event
	seen := make(map[EventID]struct{})
	for head != 0 {
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
		if event.Record != nil && event.Record.Kind == KindCompaction {
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
	if err := t.chunkFile.Sync(); err != nil {
		return err
	}
	err := t.chunkFile.Close()
	t.chunkFile = nil
	return err
}

func (t *Timeline) appendIndex(entry indexDisk) error {
	f, err := os.OpenFile(t.indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("timeline: open index: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("timeline: append index: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("timeline: sync index: %w", err)
	}
	return nil
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
			t.parents[entry.ID] = entry.Parent
			if entry.ID >= t.next {
				t.next = entry.ID + 1
			}
		case "head":
			t.head = entry.Head
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func (t *Timeline) openChunk() error {
	entries, err := filepath.Glob(filepath.Join(t.chunksRoot, "*.jsonl"))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		t.chunk = 0
	} else {
		sort.Strings(entries)
		_, err := fmt.Sscanf(filepath.Base(entries[len(entries)-1]), "%06d.jsonl", &t.chunk)
		if err != nil {
			return fmt.Errorf("timeline: parse chunk name: %w", err)
		}
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
	return err
}

func (t *Timeline) readAt(id EventID, loc EventLocation) (Event, error) {
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
	var record Record
	if disk.BlobHash != "" {
		data, readErr := os.ReadFile(filepath.Join(t.blobsRoot, disk.BlobHash))
		err = readErr
		if err != nil {
			return Event{}, fmt.Errorf("timeline: read blob %s: %w", disk.BlobHash, err)
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return Event{}, fmt.Errorf("timeline: decode blob %s: %w", disk.BlobHash, err)
		}
	} else if disk.Record != nil {
		record = *disk.Record
	} else {
		return Event{}, fmt.Errorf("timeline: event %d has no record", id)
	}
	return Event{ID: disk.ID, ParentID: disk.ParentID, Record: &record, BlobHash: disk.BlobHash}, nil
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
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil && !os.IsExist(err) {
		return "", err
	}
	return hash, nil
}
