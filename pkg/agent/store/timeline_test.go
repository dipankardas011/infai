package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestTimelineAppendLookupPathAndRotation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{ChunkBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	first, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == uuid.Nil || first.ID.Version() != 7 {
		t.Fatalf("event id=%s version=%s", first.ID, first.ID.Version())
	}
	second, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ParentID != first.ID || timeline.CurrentHeadEventID() != second.ID {
		t.Fatalf("bad ancestry: first=%+v second=%+v head=%d", first, second, timeline.CurrentHeadEventID())
	}
	got, err := timeline.LoadEvent(first.ID)
	if err != nil || got.Record == nil || first.Record == nil || got.Record.Text != first.Record.Text {
		t.Fatalf("lookup: event=%+v err=%v", got, err)
	}
	path, err := timeline.LoadFullSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].ID != first.ID || path[1].ID != second.ID {
		t.Fatalf("path=%+v", path)
	}
	entries, err := os.ReadDir(filepath.Join(root, "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected chunk rotation, found %d chunks", len(entries))
	}
}

func TestTimelineLargePayloadUsesSHA256Blob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), blobBytesThreshold)
	record := Record{Kind: KindToolResult, Text: string(payload)}
	event, err := timeline.AppendToHead(record)
	if err != nil {
		t.Fatal(err)
	}
	if event.BlobHash == "" {
		t.Fatal("large payload was not stored as a blob")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if event.BlobHash != hex.EncodeToString(digest[:]) {
		t.Fatalf("blob hash=%s want=%s", event.BlobHash, hex.EncodeToString(digest[:]))
	}
	if _, err := os.Stat(filepath.Join(root, "blobs", event.BlobHash)); err != nil {
		t.Fatal(err)
	}
	got, err := timeline.LoadEvent(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record == nil || got.Record.Text != record.Text {
		t.Fatal("blob payload changed")
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	got, err = reloaded.LoadEvent(event.ID)
	if err != nil || got.Record == nil || got.Record.Text != record.Text {
		t.Fatalf("reloaded blob event=%+v err=%v", got, err)
	}
}

func TestTimelineReloadsIndexAndHead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "persisted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.CurrentHeadEventID() != event.ID {
		t.Fatalf("head=%d want=%d", reloaded.CurrentHeadEventID(), event.ID)
	}
	got, err := reloaded.LoadEvent(event.ID)
	if err != nil || got.Record == nil || got.Record.Text != "persisted" {
		t.Fatalf("reloaded event=%+v err=%v", got, err)
	}
}

func TestTimelineActivePathStopsAtCompaction(t *testing.T) {
	timeline, err := NewTimeline(filepath.Join(t.TempDir(), "timeline"), TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	if _, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "old message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "old reply"}); err != nil {
		t.Fatal(err)
	}
	if _, err := timeline.AppendToHead(Record{Kind: KindCompaction, Compaction: &CompactionRecord{Summary: "prior context"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "new message"}); err != nil {
		t.Fatal(err)
	}

	full, err := timeline.LoadFullSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	active, err := timeline.LoadActiveSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 4 || len(active) != 2 {
		t.Fatalf("full=%d active=%d", len(full), len(active))
	}
	if active[0].Record == nil || active[0].Record.Kind != KindCompaction {
		t.Fatalf("active path did not begin at compaction: %+v", active)
	}
}

func TestTimelineBranchSelectionDoesNotMoveHeadUntilAppend(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(rootPath, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	root, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "root"})
	if err != nil {
		t.Fatal(err)
	}
	oldReply, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "old reply"})
	if err != nil {
		t.Fatal(err)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}
	timeline, err = NewTimeline(rootPath, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Selecting root is an in-memory/UI decision only. The persisted HEAD is
	// still the old reply until the new prompt is actually appended.
	selectedParent := root.ID
	if timeline.CurrentHeadEventID() != oldReply.ID {
		t.Fatalf("branch selection moved head: got=%d want=%d", timeline.CurrentHeadEventID(), oldReply.ID)
	}
	oldPath, err := timeline.LoadFullSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(oldPath) != 2 || oldPath[1].ID != oldReply.ID {
		t.Fatalf("old path changed before branch append: %+v", oldPath)
	}

	branched, err := timeline.BranchFromEventID(Record{Kind: KindMessage, Text: "branched prompt"}, selectedParent)
	if err != nil {
		t.Fatal(err)
	}
	if branched.ParentID != root.ID || timeline.CurrentHeadEventID() != branched.ID {
		t.Fatalf("branch head=%d parent=%d", timeline.CurrentHeadEventID(), branched.ParentID)
	}
	newPath, err := timeline.LoadFullSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(newPath) != 2 || newPath[0].ID != root.ID || newPath[1].ID != branched.ID {
		t.Fatalf("branched path=%+v", newPath)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}
	timeline, err = NewTimeline(rootPath, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()
	if timeline.CurrentHeadEventID() != branched.ID {
		t.Fatalf("reloaded head=%d want=%d", timeline.CurrentHeadEventID(), branched.ID)
	}
	reloadedPath, err := timeline.LoadFullSessionTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedPath) != 2 || reloadedPath[1].ID != branched.ID {
		t.Fatalf("reloaded branch path=%+v", reloadedPath)
	}
	oldPath, err = timeline.loadFullAncestryLocked(oldReply.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldPath) != 2 || oldPath[1].ID != oldReply.ID {
		t.Fatalf("original path was changed: %+v", oldPath)
	}
}

func TestTimelineRebuildsMissingIndexEntriesFromChunks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "recover me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "index.jsonl")); err != nil {
		t.Fatal(err)
	}

	recovered, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if recovered.CurrentHeadEventID() != event.ID {
		t.Fatalf("recovered head=%d want=%d", recovered.CurrentHeadEventID(), event.ID)
	}
	got, err := recovered.LoadEvent(event.ID)
	if err != nil || got.Record == nil || got.Record.Text != "recover me" {
		t.Fatalf("recovered event=%+v err=%v", got, err)
	}
}

func TestTimelineRejectsInvalidParentsAndLiveDeltas(t *testing.T) {
	timeline, err := NewTimeline(filepath.Join(t.TempDir(), "timeline"), TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()
	if _, err := timeline.BranchFromEventID(Record{Kind: KindMessage, Text: "orphan"}, uuid.New()); err == nil {
		t.Fatal("invalid parent was accepted")
	}
	if _, err := timeline.AppendToHead(Record{Kind: KindDelta, Text: "live"}); err == nil {
		t.Fatal("live delta was persisted")
	}
}

func TestTimelineRejectsCorruptedBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), blobBytesThreshold)
	event, err := timeline.AppendToHead(Record{Kind: KindToolResult, Text: string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blobs", event.BlobHash), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, err := reloaded.LoadEvent(event.ID); err == nil {
		t.Fatal("corrupted blob was accepted")
	}
}
