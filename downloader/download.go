package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type FileState int

const (
	FilePending FileState = iota
	FileDownloading
	FileResuming
	FileVerifying
	FileCompleted
	FileSkipped
	FileFailed
)

func (s FileState) String() string {
	switch s {
	case FilePending:
		return "pending"
	case FileDownloading:
		return "downloading"
	case FileResuming:
		return "resuming"
	case FileVerifying:
		return "verifying"
	case FileCompleted:
		return "completed"
	case FileSkipped:
		return "skipped"
	case FileFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type FileProgress struct {
	Path       string
	State      FileState
	Total      int64
	Downloaded int64
	Error      error
}

type OverallProgress struct {
	Files      []FileProgress
	TotalBytes int64
	DoneBytes  int64
	State      FileState
}

type Config struct {
	StagingDir string
	MaxRetries int
	RetryDelay time.Duration
	// MaxConcurrency caps the number of files downloaded in parallel.
	// Defaults to 3 when unset.
	MaxConcurrency int
}

type Downloader struct {
	client         *http.Client
	token          string
	baseURL        string
	stagingDir     string
	maxRetries     int
	retryDelay     time.Duration
	maxConcurrency int
	mu             sync.Mutex
	activeDests    map[string]bool
}

func NewDownloader(cfg Config) *Downloader {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 3
	}
	staging := filepath.Clean(cfg.StagingDir)
	if staging == "" || staging == "." {
		staging = ".infai-staging"
	}

	return &Downloader{
		client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					req.Header.Del("Authorization")
				}
				return nil
			},
		},
		stagingDir:     staging,
		maxRetries:     cfg.MaxRetries,
		retryDelay:     cfg.RetryDelay,
		maxConcurrency: cfg.MaxConcurrency,
		activeDests:    make(map[string]bool),
	}
}

func (d *Downloader) SetToken(token string) {
	d.token = token
}

func (d *Downloader) Download(ctx context.Context, plan *DownloadPlan, dest string) (<-chan OverallProgress, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, fmt.Errorf("invalid plan: %w", err)
	}

	absDest, err := filepath.Abs(dest)
	if err != nil {
		return nil, fmt.Errorf("resolve destination: %w", err)
	}

	d.mu.Lock()
	if d.activeDests[absDest] {
		d.mu.Unlock()
		return nil, fmt.Errorf("download already in progress for %q", dest)
	}
	d.activeDests[absDest] = true
	d.mu.Unlock()

	ch := make(chan OverallProgress, 1)

	go func() {
		defer close(ch)
		defer func() {
			d.mu.Lock()
			delete(d.activeDests, absDest)
			d.mu.Unlock()
		}()
		d.downloadAll(ctx, plan, absDest, ch)
	}()

	return ch, nil
}

type downloadState struct {
	mu       sync.Mutex
	progress *OverallProgress
	ch       chan<- OverallProgress
}

func (s *downloadState) snapshot() OverallProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := *s.progress
	p.Files = append([]FileProgress(nil), s.progress.Files...)
	return p
}

func (s *downloadState) send() {
	s.ch <- s.snapshot()
}

func (s *downloadState) update(fn func(*OverallProgress)) {
	s.mu.Lock()
	fn(s.progress)
	s.mu.Unlock()
	s.send()
}

func recomputeDoneBytes(p *OverallProgress) {
	var done int64
	for _, f := range p.Files {
		done += f.Downloaded
	}
	p.DoneBytes = done
}

type progressReporter struct {
	state    *downloadState
	fileIdx  int
	lastSend time.Time
	interval time.Duration
}

func (r *progressReporter) onBytes(n int64) {
	r.state.mu.Lock()
	r.state.progress.Files[r.fileIdx].Downloaded = n
	recomputeDoneBytes(r.state.progress)
	snap := *r.state.progress
	snap.Files = append([]FileProgress(nil), r.state.progress.Files...)
	r.state.mu.Unlock()
	if now := time.Now(); now.Sub(r.lastSend) >= r.interval {
		r.lastSend = now
		r.state.ch <- snap
	}
}

func (d *Downloader) downloadAll(ctx context.Context, plan *DownloadPlan, dest string, ch chan<- OverallProgress) {
	combined := plan.CombinedFiles()
	progress := &OverallProgress{
		Files: make([]FileProgress, 0, len(combined)),
		State: FilePending,
	}
	for _, f := range combined {
		progress.Files = append(progress.Files, FileProgress{
			Path:  f.Path,
			State: FilePending,
			Total: f.Size,
		})
		progress.TotalBytes += f.Size
	}
	state := &downloadState{progress: progress, ch: ch}

	stagingRoot := filepath.Join(dest, d.stagingDir)
	if err := os.MkdirAll(stagingRoot, 0755); err != nil {
		state.update(func(p *OverallProgress) {
			p.State = FileFailed
			for i := range p.Files {
				p.Files[i].State = FileFailed
				p.Files[i].Error = fmt.Errorf("staging dir: %w", err)
			}
		})
		return
	}

	if err := checkDiskSpace(dest, plan.CombinedBytes()); err != nil {
		state.update(func(p *OverallProgress) {
			p.State = FileFailed
			for i := range p.Files {
				p.Files[i].State = FileFailed
				p.Files[i].Error = err
			}
		})
		return
	}

	workers := d.maxConcurrency
	if workers <= 0 {
		workers = 1
	}
	if workers > len(combined) {
		workers = len(combined)
	}

	type result struct {
		idx    int
		staged stagedFile
		err    error
	}
	jobs := make(chan int, len(combined))
	for i := range combined {
		jobs <- i
	}
	close(jobs)
	results := make(chan result, len(combined))

	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for i := range jobs {
			pf := combined[i]
			state.update(func(p *OverallProgress) { p.Files[i].State = FileDownloading })

			reporter := &progressReporter{state: state, fileIdx: i, interval: 200 * time.Millisecond}
			staged, err := d.downloadFileWithRetry(ctx, plan, pf, stagingRoot, reporter)
			if err != nil {
				state.update(func(p *OverallProgress) {
					p.Files[i].State = FileFailed
					p.Files[i].Error = err
					p.State = FileFailed
				})
				results <- result{idx: i, err: err}
				continue
			}

			state.update(func(p *OverallProgress) { p.Files[i].State = FileVerifying })
			if err := verifyChecksum(staged.tmpPath, pf.SHA256); err != nil {
				checksumErr := fmt.Errorf("checksum mismatch for %s: %w", pf.Path, err)
				state.update(func(p *OverallProgress) {
					p.Files[i].State = FileFailed
					p.Files[i].Error = checksumErr
					p.State = FileFailed
				})
				results <- result{idx: i, err: checksumErr}
				continue
			}

			state.update(func(p *OverallProgress) {
				p.Files[i].State = FileCompleted
				p.Files[i].Downloaded = pf.Size
				recomputeDoneBytes(p)
			})
			results <- result{idx: i, staged: staged}
		}
	}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go worker()
	}
	wg.Wait()
	close(results)

	ordered := make([]stagedFile, len(combined))
	failed := false
	for r := range results {
		if r.err != nil {
			failed = true
			continue
		}
		ordered[r.idx] = r.staged
	}
	if ctx.Err() != nil {
		failed = true
	}

	if failed {
		state.update(func(p *OverallProgress) { p.State = FileFailed })
		return
	}

	for _, sf := range ordered {
		destPath := filepath.Join(dest, filepath.Base(sf.origPath))
		if err := os.Rename(sf.tmpPath, destPath); err != nil {
			state.update(func(p *OverallProgress) {
				p.State = FileFailed
				for j := range p.Files {
					if p.Files[j].State != FileFailed {
						p.Files[j].State = FileFailed
						p.Files[j].Error = fmt.Errorf("publish %s: %w", sf.origPath, err)
					}
				}
			})
			return
		}
	}

	state.update(func(p *OverallProgress) { p.State = FileCompleted })
}

type stagedFile struct {
	origPath string
	tmpPath  string
}

func (d *Downloader) downloadFileWithRetry(ctx context.Context, plan *DownloadPlan, pf PlanFile, stagingRoot string, reporter *progressReporter) (stagedFile, error) {
	var lastErr error
	for attempt := 0; attempt < d.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return stagedFile{}, ctx.Err()
			case <-time.After(d.retryDelay):
			}
		}

		sf, err := d.downloadFile(ctx, plan, pf, stagingRoot, reporter)
		if err == nil {
			return sf, nil
		}

		lastErr = err
		if ctx.Err() != nil {
			return stagedFile{}, ctx.Err()
		}

		if isPermanent(err) {
			return stagedFile{}, err
		}
	}
	return stagedFile{}, fmt.Errorf("download %s failed after %d attempts: %w", pf.Path, d.maxRetries, lastErr)
}

func (d *Downloader) downloadFile(ctx context.Context, plan *DownloadPlan, pf PlanFile, stagingRoot string, reporter *progressReporter) (stagedFile, error) {
	destName := filepath.Base(pf.Path)
	tmpPath := filepath.Join(stagingRoot, destName+".partial")

	existing, err := os.Stat(tmpPath)
	if err == nil && existing.Size() > 0 && existing.Size() < pf.Size {
		return d.resumeDownload(ctx, plan, pf, tmpPath, existing.Size(), reporter)
	}

	return d.freshDownload(ctx, plan, pf, tmpPath, reporter)
}

func (d *Downloader) freshDownload(ctx context.Context, plan *DownloadPlan, pf PlanFile, tmpPath string, reporter *progressReporter) (stagedFile, error) {
	url := d.buildDownloadURL(plan.RepoID, plan.Revision, pf.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stagedFile{}, err
	}
	d.setAuth(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return stagedFile{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return stagedFile{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if resp.ContentLength > 0 && resp.ContentLength != pf.Size {
		return stagedFile{}, fmt.Errorf("size mismatch: expected %d, server reports %d", pf.Size, resp.ContentLength)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return stagedFile{}, fmt.Errorf("create partial file: %w", err)
	}
	defer f.Close()

	cr := &countingReader{r: resp.Body, reporter: reporter}
	if _, err := io.Copy(f, cr); err != nil {
		return stagedFile{}, fmt.Errorf("stream download: %w", err)
	}

	return stagedFile{origPath: pf.Path, tmpPath: tmpPath}, nil
}

func (d *Downloader) resumeDownload(ctx context.Context, plan *DownloadPlan, pf PlanFile, tmpPath string, existingSize int64, reporter *progressReporter) (stagedFile, error) {
	url := d.buildDownloadURL(plan.RepoID, plan.Revision, pf.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stagedFile{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	d.setAuth(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return stagedFile{}, fmt.Errorf("resume request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return d.freshDownload(ctx, plan, pf, tmpPath, reporter)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return stagedFile{}, fmt.Errorf("unexpected resume status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return stagedFile{}, fmt.Errorf("open partial for resume: %w", err)
	}
	defer f.Close()

	cr := &countingReader{r: resp.Body, base: existingSize, reporter: reporter}
	if _, err := io.Copy(f, cr); err != nil {
		return stagedFile{}, fmt.Errorf("resume stream: %w", err)
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return stagedFile{}, err
	}
	if info.Size() != pf.Size {
		return stagedFile{}, fmt.Errorf("resumed file size mismatch: got %d, want %d", info.Size(), pf.Size)
	}

	return stagedFile{origPath: pf.Path, tmpPath: tmpPath}, nil
}

type countingReader struct {
	r        io.Reader
	n        int64
	base     int64
	reporter *progressReporter
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	if cr.reporter != nil {
		cr.reporter.onBytes(cr.base + cr.n)
	}
	return n, err
}

func (d *Downloader) setAuth(req *http.Request) {
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
}

func (d *Downloader) buildDownloadURL(repoID, revision, filePath string) string {
	rev := revision
	if rev == "" {
		rev = "main"
	}
	base := d.baseURL
	if base == "" {
		base = "https://huggingface.co"
	}
	return fmt.Sprintf("%s/%s/resolve/%s/%s", base, repoID, rev, filePath)
}

func verifyChecksum(path, wantSHA256 string) error {
	if wantSHA256 == "" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash for checksum: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got[:16], wantSHA256[:16])
	}
	return nil
}

func checkDiskSpace(dest string, needed int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dest, &stat); err != nil {
		return nil
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if available < needed+100*1024*1024 {
		return fmt.Errorf("insufficient disk space: need %s, available %s", formatBytes(needed), formatBytes(available))
	}
	return nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "checksum mismatch"):
		return true
	case strings.Contains(msg, "size mismatch"):
		return true
	case strings.Contains(msg, "insufficient disk"):
		return true
	default:
		return false
	}
}
