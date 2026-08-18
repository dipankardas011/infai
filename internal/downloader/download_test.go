package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dipankardas011/infai/internal/model"
)

type testFileServer struct {
	mu        sync.Mutex
	content   []byte
	sha256    string
	failFirst int
	calls     int
	noRange   bool
}

func (s *testFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if s.failFirst > 0 && calls <= s.failFirst {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" && !s.noRange {
		var start int64
		fmt.Sscanf(rangeHdr, "bytes=%d-", &start)
		if start >= 0 && start < int64(len(s.content)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(s.content)-1, len(s.content)))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(s.content)-int(start)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(s.content[start:])
			return
		}
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(s.content)))
	w.WriteHeader(http.StatusOK)
	w.Write(s.content)
}

func testContent(size int64) ([]byte, string) {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:])
}

func newTestDownloader(t *testing.T) (*Downloader, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		StagingDir: filepath.Join(dir, ".staging"),
		MaxRetries: 2,
		RetryDelay: 10 * time.Millisecond,
	}
	return NewDownloader(cfg), dir
}

func TestDownloadSingleFile(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: sha}},
		TotalBytes: 4096,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s: %+v", last.State, last)
	}

	finalPath := filepath.Join(dest, "model.gguf")
	stat, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("file not published: %v", err)
	}
	if stat.Size() != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", stat.Size())
	}
}

func TestDownloadOptionalFiles(t *testing.T) {
	content, sha := testContent(2048)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:        "org/repo",
		Revision:      "abc123",
		EngineKind:    model.EngineLlamaCPP,
		Files:         []PlanFile{{Path: "model.gguf", Size: 2048, SHA256: sha}},
		OptionalFiles: []PlanFile{{Path: "mmproj.gguf", Size: 2048, SHA256: sha}},
		TotalBytes:    4096,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s: %+v", last.State, last)
	}
	if len(last.Files) != 2 {
		t.Fatalf("expected 2 files in progress, got %d", len(last.Files))
	}
	for _, name := range []string{"model.gguf", "mmproj.gguf"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("file %s not published: %v", name, err)
		}
	}
}

func TestDownloadVLLMPlan(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineVLLM,
		Files: []PlanFile{
			{Path: "config.json", Size: 4096, SHA256: sha},
			{Path: "tokenizer.json", Size: 4096, SHA256: sha},
			{Path: "model.safetensors", Size: 4096, SHA256: sha},
		},
		TotalBytes: 12288,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s: %+v", last.State, last)
	}
	if len(last.Files) != 3 {
		t.Fatalf("expected 3 files in progress, got %d", len(last.Files))
	}
	for _, name := range []string{"config.json", "tokenizer.json", "model.safetensors"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("file %s not published: %v", name, err)
		}
	}
}

func TestDownloadParallel(t *testing.T) {
	content, sha := testContent(4096)

	var mu sync.Mutex
	var active, maxActive int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineVLLM,
		Files: []PlanFile{
			{Path: "a.safetensors", Size: 4096, SHA256: sha},
			{Path: "b.safetensors", Size: 4096, SHA256: sha},
			{Path: "c.safetensors", Size: 4096, SHA256: sha},
		},
		TotalBytes: 12288,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s: %+v", last.State, last)
	}

	mu.Lock()
	peak := maxActive
	mu.Unlock()
	if peak < 2 {
		t.Fatalf("expected parallel downloads, peak concurrency was %d", peak)
	}
}

func TestDownloadResume(t *testing.T) {
	content, sha := testContent(8192)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	stagingRoot := filepath.Join(dest, d.stagingDir)
	os.MkdirAll(stagingRoot, 0755)
	partialPath := filepath.Join(stagingRoot, "model.gguf.partial")
	os.WriteFile(partialPath, content[:4096], 0644)

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 8192, SHA256: sha}},
		TotalBytes: 8192,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s", last.State)
	}

	finalPath := filepath.Join(dest, "model.gguf")
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	if len(data) != 8192 {
		t.Fatalf("expected 8192 bytes, got %d", len(data))
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	content, _ := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: "deadbeef00000000000000000000000000000000000000000000000000000000"}},
		TotalBytes: 4096,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileFailed {
		t.Fatalf("expected failed, got %s", last.State)
	}
	if last.Files[0].Error == nil {
		t.Fatal("expected checksum error")
	}
	if !strings.Contains(last.Files[0].Error.Error(), "checksum") {
		t.Fatalf("expected checksum error, got: %v", last.Files[0].Error)
	}

	finalPath := filepath.Join(dest, "model.gguf")
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("file should not be published after checksum failure")
	}
}

func TestDownloadCancelMidway(t *testing.T) {
	content, sha := testContent(102400)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 102400, SHA256: sha}},
		TotalBytes: 102400,
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := d.Download(ctx, plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	cancel()

	var last OverallProgress
	for p := range ch {
		last = p
	}

	if last.State == FileCompleted {
		t.Fatal("should not complete after cancel")
	}

	finalPath := filepath.Join(dest, "model.gguf")
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("file should not be published after cancel")
	}

	partialPath := filepath.Join(dest, d.stagingDir, "model.gguf.partial")
	if _, err := os.Stat(partialPath); os.IsNotExist(err) {
		t.Log("staging data may have been cleaned (acceptable)")
	}
}

func TestDownloadRetryOnFailure(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha, failFirst: 1})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: sha}},
		TotalBytes: 4096,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed after retry, got %s: %v", last.State, last.Files[0].Error)
	}
}

func TestDownloadSizeMismatch(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 9999999, SHA256: sha}},
		TotalBytes: 9999999,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileFailed {
		t.Fatalf("expected failed for size mismatch, got %s", last.State)
	}
}

func TestDownloadMultipleFiles(t *testing.T) {
	c1, s1 := testContent(2048)
	c2, s2 := testContent(1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/model.gguf") {
			w.Header().Set("Content-Length", "2048")
			w.Write(c1)
		} else if strings.HasSuffix(r.URL.Path, "/tokenizer.json") {
			w.Header().Set("Content-Length", "1024")
			w.Write(c2)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineVLLM,
		Files: []PlanFile{
			{Path: "model.gguf", Size: 2048, SHA256: s1},
			{Path: "tokenizer.json", Size: 1024, SHA256: s2},
		},
		TotalBytes: 3072,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed, got %s", last.State)
	}
	if last.DoneBytes != 3072 {
		t.Fatalf("expected 3072 done, got %d", last.DoneBytes)
	}

	for _, name := range []string{"model.gguf", "tokenizer.json"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("missing published file %s: %v", name, err)
		}
	}
}

func TestDownloadConcurrentRejected(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: sha}},
		TotalBytes: 4096,
	}

	ch, err := d.Download(context.Background(), plan, dest)
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	defer func() {
		for range ch {
		}
	}()

	_, err = d.Download(context.Background(), plan, dest)
	if err == nil {
		t.Fatal("expected concurrent download rejection")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected concurrent rejection, got: %v", err)
	}
}

func TestDownloadWithoutChecksum(t *testing.T) {
	content, _ := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096}},
		TotalBytes: 4096,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed without checksum, got %s", last.State)
	}
}

func TestDownloadAuthNotInErrors(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.SetToken("hf_secret_test_token")
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: "badchecksum0000000000000000000000000000000000000000000000000000"}},
		TotalBytes: 4096,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.Files[0].Error == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(last.Files[0].Error.Error(), "hf_secret") {
		t.Fatal("token leaked in error")
	}
	if strings.Contains(last.Files[0].Error.Error(), "Bearer") {
		t.Fatal("auth header leaked in error")
	}
}

func TestDownloadResumeServerNoRange(t *testing.T) {
	content, sha := testContent(8192)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha, noRange: true})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	stagingRoot := filepath.Join(dest, d.stagingDir)
	os.MkdirAll(stagingRoot, 0755)
	partialPath := filepath.Join(stagingRoot, "model.gguf.partial")
	os.WriteFile(partialPath, content[:4096], 0644)

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 8192, SHA256: sha}},
		TotalBytes: 8192,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed (fallback to fresh download), got %s", last.State)
	}
}

func TestValidatePlanInDownload(t *testing.T) {
	d, _ := newTestDownloader(t)
	_, err := d.Download(context.Background(), nil, "/tmp")
	if err == nil {
		t.Fatal("expected validation error for nil plan")
	}
}

func TestStagingDirNotPublished(t *testing.T) {
	content, sha := testContent(4096)
	srv := httptest.NewServer(&testFileServer{content: content, sha256: sha})
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 4096, SHA256: sha}},
		TotalBytes: 4096,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	for range ch {
	}

	if _, err := os.Stat(filepath.Join(dest, ".staging")); err == nil {
		t.Fatal("staging dir should not contain published files; .partial should be gone")
	}

	partialPath := filepath.Join(dest, d.stagingDir, "model.gguf.partial")
	if _, err := os.Stat(partialPath); err == nil {
		t.Fatal("partial file should not exist after successful publish")
	}
}

func TestDownloadFailedShardPreventsPublish(t *testing.T) {
	c1, s1 := testContent(2048)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/shard-1.gguf") {
			w.Header().Set("Content-Length", "2048")
			w.Write(c1)
		} else if strings.HasSuffix(r.URL.Path, "/shard-2.gguf") {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files: []PlanFile{
			{Path: "shard-1.gguf", Size: 2048, SHA256: s1},
			{Path: "shard-2.gguf", Size: 4096},
		},
		TotalBytes: 6144,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}

	if last.State != FileFailed {
		t.Fatalf("expected failed, got %s", last.State)
	}

	if _, err := os.Stat(filepath.Join(dest, "shard-1.gguf")); err == nil {
		t.Fatal("shard-1 should not be published when shard-2 fails")
	}
}

func TestCheckDiskSpace(t *testing.T) {
	err := checkDiskSpace(t.TempDir(), 1)
	if err != nil {
		t.Fatalf("unexpected disk space error: %v", err)
	}

	err = checkDiskSpace(t.TempDir(), 1<<60)
	if err != nil {
		t.Logf("expected disk space warning for unrealistic size: %v", err)
	}
}

func TestBuildDownloadURL(t *testing.T) {
	d, _ := newTestDownloader(t)
	u := d.buildDownloadURL("org/repo", "abc123", "model.gguf")
	if u != "https://huggingface.co/org/repo/resolve/abc123/model.gguf" {
		t.Fatalf("unexpected url: %s", u)
	}

	u = d.buildDownloadURL("org/repo", "", "model.gguf")
	if !strings.Contains(u, "/main/") {
		t.Fatalf("expected main fallback, got %s", u)
	}
}

func TestVerifyChecksumNoWant(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("hello"), 0644)
	if err := verifyChecksum(p, ""); err != nil {
		t.Fatalf("empty checksum should pass: %v", err)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.b)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %s, want %s", tt.b, got, tt.want)
		}
	}
}

func TestFileStateString(t *testing.T) {
	if FilePending.String() != "pending" {
		t.Fatal("pending string")
	}
	if FileDownloading.String() != "downloading" {
		t.Fatal("downloading string")
	}
	if FileCompleted.String() != "completed" {
		t.Fatal("completed string")
	}
	if FileFailed.String() != "failed" {
		t.Fatal("failed string")
	}
}

func TestDownloadEmptyPlanFiles(t *testing.T) {
	d, dest := newTestDownloader(t)
	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc",
		EngineKind: model.EngineLlamaCPP,
		Files:      nil,
	}
	_, err := d.Download(context.Background(), plan, dest)
	if err == nil {
		t.Fatal("expected validation error for empty files")
	}
}

func TestIsPermanent(t *testing.T) {
	if !isPermanent(fmt.Errorf("checksum mismatch")) {
		t.Fatal("checksum should be permanent")
	}
	if !isPermanent(fmt.Errorf("size mismatch")) {
		t.Fatal("size should be permanent")
	}
	if isPermanent(fmt.Errorf("network timeout")) {
		t.Fatal("network should not be permanent")
	}
}

func TestResumeServerReturns200(t *testing.T) {
	content, sha := testContent(8192)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusOK)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer srv.Close()

	d, dest := newTestDownloader(t)
	d.client = srv.Client()
	d.baseURL = srv.URL

	stagingRoot := filepath.Join(dest, d.stagingDir)
	os.MkdirAll(stagingRoot, 0755)
	partialPath := filepath.Join(stagingRoot, "model.gguf.partial")
	os.WriteFile(partialPath, content[:4096], 0644)

	plan := &DownloadPlan{
		RepoID:     "org/repo",
		Revision:   "abc123",
		EngineKind: model.EngineLlamaCPP,
		Files:      []PlanFile{{Path: "model.gguf", Size: 8192, SHA256: sha}},
		TotalBytes: 8192,
	}

	ch, _ := d.Download(context.Background(), plan, dest)
	var last OverallProgress
	for p := range ch {
		last = p
	}
	if last.State != FileCompleted {
		t.Fatalf("expected completed after 200 fallback, got %s: %v", last.State, last.Files[0].Error)
	}
}

func TestConfigDefaults(t *testing.T) {
	d := NewDownloader(Config{})
	if d.maxRetries != 3 {
		t.Fatalf("expected 3 retries, got %d", d.maxRetries)
	}
	if d.retryDelay != 2*time.Second {
		t.Fatalf("expected 2s delay, got %v", d.retryDelay)
	}
	if d.stagingDir != ".infai-staging" {
		t.Fatalf("expected .infai-staging, got %s", d.stagingDir)
	}
}
