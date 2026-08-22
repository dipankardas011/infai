package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrNotFound is returned when a requested session transcript does not exist.
var ErrNotFound = errors.New("store: session not found")

// Sink is a live consumer of records. Deltas flow to sinks (the user's SSE
// response or stdout); durable kinds additionally land in the transcript file.
type Sink struct {
	Write func(Record) error
}

// Recorder is the multi-writer for one session. Each Record is sent to every
// registered sink (live streaming) and, unless it is a delta, appended to the
// transcript file. Records are buffered and only become durable when Sync
// succeeds (flush + fsync); the engine calls Sync once per chat turn, so a
// finished turn is always on disk.
type Recorder struct {
	file  *os.File
	buf   *bufio.Writer
	mu    sync.Mutex
	sinks map[int]Sink
	next  int
}

// newRecorderAppend opens the transcript file in append mode, creating it if
// needed.
func newRecorderAppend(path string) (*Recorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: open session: %w", err)
	}
	return &Recorder{file: f, buf: bufio.NewWriter(f), sinks: make(map[int]Sink)}, nil
}

// NewLiveRecorder creates a sink fan-out without a legacy transcript file.
// Timeline owns durable session history; this recorder is only for streaming.
func NewLiveRecorder() *Recorder {
	return &Recorder{sinks: make(map[int]Sink)}
}

// AddSink registers a live consumer and returns a function that removes it.
func (r *Recorder) AddSink(fn func(Record) error) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.next
	r.next++
	r.sinks[id] = Sink{Write: fn}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.sinks, id)
	}
}

// Record sends a record to the sinks and, for durable kinds, appends it to the
// transcript buffer. It returns any persistence error. The record is not
// durable until Sync succeeds.
func (r *Recorder) Record(rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if rec.Kind != KindDelta && r.file != nil {
		if err := r.appendLocked(rec); err != nil {
			return err
		}
	}
	for _, s := range r.sinks {
		_ = s.Write(rec)
	}
	return nil
}

// appendLocked writes one JSON line into the write buffer. Caller holds r.mu.
func (r *Recorder) appendLocked(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode record: %w", err)
	}
	if _, err := r.buf.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("store: write session: %w", err)
	}
	return nil
}

// Sync flushes buffered records and fsyncs the file, making everything
// recorded so far durable. Safe to call more than once.
func (r *Recorder) Sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.syncLocked()
}

func (r *Recorder) syncLocked() error {
	if r.file == nil {
		return nil
	}
	if err := r.buf.Flush(); err != nil {
		return fmt.Errorf("store: flush session: %w", err)
	}
	if err := r.file.Sync(); err != nil {
		return fmt.Errorf("store: sync session: %w", err)
	}
	return nil
}

// Close flushes, syncs and closes the transcript file. Safe to call more than
// once.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	syncErr := r.syncLocked()
	closeErr := r.file.Close()
	r.file = nil
	r.buf = nil
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return fmt.Errorf("store: close session: %w", closeErr)
	}
	return nil
}
