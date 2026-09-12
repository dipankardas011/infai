package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
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
	path, err := timeline.LoadActiveBranchContext()
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

func TestTimelineBranchesFromSelectedEarlierAssistant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	_, err = timeline.AppendToHead(Record{Kind: KindMessage, Text: "Hi Bro"})
	if err != nil {
		t.Fatal(err)
	}
	selectedAssistant, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "Hi there! How can I help you today?"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = timeline.AppendToHead(Record{Kind: KindMessage, Text: "tell me why programming languages?"})
	if err != nil {
		t.Fatal(err)
	}
	oldLast, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "That is a huge and fascinating question!"})
	if err != nil {
		t.Fatal(err)
	}

	branched, err := timeline.BranchFromEventID(Record{Kind: KindMessage, Text: "How was your day?"}, selectedAssistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if branched.ParentID != selectedAssistant.ID {
		t.Fatalf("branch parent=%s want selected assistant=%s", branched.ParentID, selectedAssistant.ID)
	}
	if branched.BranchFrom == nil || *branched.BranchFrom != selectedAssistant.ID {
		t.Fatalf("branch_from=%v want selected assistant=%s", branched.BranchFrom, selectedAssistant.ID)
	}
	if branched.ParentID == oldLast.ID {
		t.Fatalf("branched from old last event %s", oldLast.ID)
	}

	path, err := timeline.LoadActiveBranchContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 3 || path[1].ID != selectedAssistant.ID || path[2].ID != branched.ID {
		t.Fatalf("active branch path=%+v", path)
	}
}

func TestTimelinePersistsHeadSeparately(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Text: "persist head"})
	if err != nil {
		t.Fatal(err)
	}
	if err := timeline.Close(); err != nil {
		t.Fatal(err)
	}

	head, err := os.ReadFile(filepath.Join(root, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(head)) != event.ID.String() {
		t.Fatalf("HEAD=%q want=%s", bytes.TrimSpace(head), event.ID)
	}
	index, err := os.ReadFile(filepath.Join(root, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(index, []byte(`"type":"head"`)) {
		t.Fatal("index.jsonl contains a HEAD record")
	}

	reloaded, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if got := reloaded.CurrentHeadEventID(); got != event.ID {
		t.Fatalf("reloaded HEAD=%s want=%s", got, event.ID)
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

	full, err := timeline.LoadEntireTimeline()
	if err != nil {
		t.Fatal(err)
	}
	active, err := timeline.LoadActiveBranchContext()
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
	oldPath, err := timeline.LoadActiveBranchContext()
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
	newPath, err := timeline.LoadActiveBranchContext()
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
	reloadedPath, err := timeline.LoadActiveBranchContext()
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

func TestTimelineDecodesLegacyTextOnlyMessage(t *testing.T) {
	legacy := []byte(`{"kind":"message","ts":"2024-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}`)
	var record Record
	if err := json.Unmarshal(legacy, &record); err != nil {
		t.Fatal(err)
	}
	if record.Message == nil || record.Message.Text() != "hello" {
		t.Fatalf("legacy message decoded wrong: %+v", record.Message)
	}
	if len(record.Message.Images) != 0 {
		t.Fatalf("legacy message unexpectedly carried images: %+v", record.Message.Images)
	}
}

func TestTimelinePersistsImageMessageAcrossReload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	message := contracts.NewUserMessageWithInput(contracts.UserInput{
		Text: "describe",
		Images: []contracts.ImageInput{{
			Name: "clipboard-1.png", MediaType: "image/png", Width: 2, Height: 2, Data: []byte("small-png"),
		}},
	})
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Timestamp: time.Now().UTC(), Message: &message})
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
	got, err := reloaded.LoadEvent(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record == nil || got.Record.Message == nil {
		t.Fatal("image message missing after reload")
	}
	images := got.Record.Message.Images
	if len(images) != 1 {
		t.Fatalf("images=%d want 1", len(images))
	}
	if images[0].Name != "clipboard-1.png" || images[0].MediaType != "image/png" {
		t.Fatalf("image metadata changed: %+v", images[0])
	}
	if !bytes.Equal(images[0].Data, []byte("small-png")) {
		t.Fatal("image bytes changed across reload")
	}
}

func TestTimelineAlwaysBlobsImageMessages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	message := contracts.NewUserMessageWithInput(contracts.UserInput{
		Text: "tiny",
		Images: []contracts.ImageInput{{
			Name: "tiny.png", MediaType: "image/png", Width: 1, Height: 1, Data: []byte("tiny-image-bytes"),
		}},
	})
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Timestamp: time.Now().UTC(), Message: &message})
	if err != nil {
		t.Fatal(err)
	}
	if event.BlobHash == "" {
		t.Fatal("small image message was not stored as a blob")
	}
	if _, err := os.Stat(filepath.Join(root, "blobs", event.BlobHash)); err != nil {
		t.Fatalf("blob file missing: %v", err)
	}
	chunks, err := os.ReadFile(filepath.Join(root, "chunks", "000000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(chunks, []byte("tiny-image-bytes")) {
		t.Fatal("chunk contains raw image bytes")
	}
	if bytes.Contains(chunks, []byte(base64.StdEncoding.EncodeToString([]byte("tiny-image-bytes")))) {
		t.Fatal("chunk contains base64 image bytes")
	}
	if !bytes.Contains(chunks, []byte(event.BlobHash)) {
		t.Fatal("chunk does not reference the blob hash")
	}
	got, err := timeline.LoadEvent(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record == nil || got.Record.Message == nil || len(got.Record.Message.Images) != 1 {
		t.Fatalf("blob-backed image message did not resolve: %+v", got)
	}
}

func TestTimelineStoresLargeImageMessageInBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "timeline")
	timeline, err := NewTimeline(root, TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), blobBytesThreshold)
	message := contracts.NewUserMessageWithInput(contracts.UserInput{
		Text: "big",
		Images: []contracts.ImageInput{{
			Name: "big.png", MediaType: "image/png", Width: 1, Height: 1, Data: payload,
		}},
	})
	event, err := timeline.AppendToHead(Record{Kind: KindMessage, Timestamp: time.Now().UTC(), Message: &message})
	if err != nil {
		t.Fatal(err)
	}
	if event.BlobHash == "" {
		t.Fatal("large image message was not stored as a blob")
	}
	got, err := timeline.LoadEvent(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record == nil || got.Record.Message == nil || len(got.Record.Message.Images) != 1 {
		t.Fatal("blob-backed image message did not resolve")
	}
	if len(got.Record.Message.Images[0].Data) != len(payload) {
		t.Fatalf("image bytes length=%d want=%d", len(got.Record.Message.Images[0].Data), len(payload))
	}
}

func TestTimelineBlobEventCarriesBoundedPreview(t *testing.T) {
	timeline, err := NewTimeline(filepath.Join(t.TempDir(), "timeline"), TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	message := contracts.NewUserMessageWithInput(contracts.UserInput{
		Text: "describe",
		Images: []contracts.ImageInput{{
			Name: "a.png", MediaType: "image/png", Width: 2, Height: 2, Data: []byte("bytes"),
		}},
	})
	if _, err := timeline.AppendToHead(Record{Kind: KindMessage, Timestamp: time.Now().UTC(), Message: &message}); err != nil {
		t.Fatal(err)
	}
	events, err := timeline.LoadEntireTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Record != nil {
		t.Fatalf("expected one unresolved blob event: %+v", events)
	}
	preview := events[0].Preview
	if preview == nil {
		t.Fatal("blob event has no preview sidecar")
	}
	if preview.Role != "user" || preview.Text != "describe" || preview.ImageCount != 1 {
		t.Fatalf("preview=%+v want role=user text=describe images=1", preview)
	}

	// Full bytes are still recoverable from the blob.
	resolved, err := timeline.LoadEvent(events[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Record == nil || resolved.Record.Message == nil || len(resolved.Record.Message.Images) != 1 {
		t.Fatalf("blob did not resolve: %+v", resolved.Record)
	}
	if len(resolved.Record.Message.Images[0].Data) == 0 {
		t.Fatal("resolved image bytes are empty")
	}
}

func TestTimelinePreviewIsBounded(t *testing.T) {
	timeline, err := NewTimeline(filepath.Join(t.TempDir(), "timeline"), TimelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer timeline.Close()

	payload := bytes.Repeat([]byte("x"), blobBytesThreshold)
	if _, err := timeline.AppendToHead(Record{Kind: KindToolResult, Timestamp: time.Now().UTC(), ToolResult: &ToolResultRecord{CallID: "c", Status: "success", Output: string(payload)}}); err != nil {
		t.Fatal(err)
	}
	events, err := timeline.LoadEntireTimeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Preview == nil {
		t.Fatalf("expected a preview for the blob event: %+v", events)
	}
	if got := len([]rune(events[0].Preview.Text)); got > previewTextLimit+1 {
		t.Fatalf("preview text length=%d want <= %d", got, previewTextLimit+1)
	}
}
