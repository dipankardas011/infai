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
}

type Downloader struct {
	client      *http.Client
	token       string
	baseURL     string
	stagingDir  string
	maxRetries  int
	retryDelay  time.Duration
	mu          sync.Mutex
	activeDests map[string]bool
}

func NewDownloader(cfg Config) *Downloader {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
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
		stagingDir:  staging,
		maxRetries:  cfg.MaxRetries,
		retryDelay:  cfg.RetryDelay,
		activeDests: make(map[string]bool),
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

func (d *Downloader) downloadAll(ctx context.Context, plan *DownloadPlan, dest string, ch chan<- OverallProgress) {
	progress := OverallProgress{
		Files: make([]FileProgress, 0, len(plan.Files)),
		State: FilePending,
	}
	for _, f := range plan.Files {
		progress.Files = append(progress.Files, FileProgress{
			Path:  f.Path,
			State: FilePending,
			Total: f.Size,
		})
		progress.TotalBytes += f.Size
	}

	stagingRoot := filepath.Join(dest, d.stagingDir)
	if err := os.MkdirAll(stagingRoot, 0755); err != nil {
		progress.State = FileFailed
		for i := range progress.Files {
			progress.Files[i].State = FileFailed
			progress.Files[i].Error = fmt.Errorf("staging dir: %w", err)
		}
		ch <- progress
		return
	}

	if err := checkDiskSpace(dest, plan.TotalBytes); err != nil {
		progress.State = FileFailed
		for i := range progress.Files {
			progress.Files[i].State = FileFailed
			progress.Files[i].Error = err
		}
		ch <- progress
		return
	}

	var downloaded []stagedFile
	failed := false

	for i, pf := range plan.Files {
		if err := ctx.Err(); err != nil {
			failed = true
			progress.Files[i].State = FileFailed
			progress.Files[i].Error = err
			progress.State = FileFailed
			ch <- progress
			return
		}

		progress.Files[i].State = FileDownloading
		ch <- progress

		staged, err := d.downloadFileWithRetry(ctx, plan, pf, stagingRoot)
		if err != nil {
			failed = true
			progress.Files[i].State = FileFailed
			progress.Files[i].Error = err
			progress.State = FileFailed
			ch <- progress
			continue
		}

		progress.Files[i].State = FileVerifying
		ch <- progress

		if err := verifyChecksum(staged.tmpPath, pf.SHA256); err != nil {
			failed = true
			progress.Files[i].State = FileFailed
			progress.Files[i].Error = fmt.Errorf("checksum mismatch for %s: %w", pf.Path, err)
			progress.State = FileFailed
			ch <- progress
			continue
		}

		progress.Files[i].State = FileCompleted
		progress.Files[i].Downloaded = pf.Size
		progress.DoneBytes += pf.Size
		downloaded = append(downloaded, staged)
		ch <- progress
	}

	if failed {
		progress.State = FileFailed
		ch <- progress
		return
	}

	for _, sf := range downloaded {
		destPath := filepath.Join(dest, filepath.Base(sf.origPath))
		if err := os.Rename(sf.tmpPath, destPath); err != nil {
			progress.State = FileFailed
			for j := range progress.Files {
				if progress.Files[j].State != FileFailed {
					progress.Files[j].State = FileFailed
					progress.Files[j].Error = fmt.Errorf("publish %s: %w", sf.origPath, err)
				}
			}
			ch <- progress
			return
		}
	}

	progress.State = FileCompleted
	ch <- progress
}

type stagedFile struct {
	origPath string
	tmpPath  string
}

func (d *Downloader) downloadFileWithRetry(ctx context.Context, plan *DownloadPlan, pf PlanFile, stagingRoot string) (stagedFile, error) {
	var lastErr error
	for attempt := 0; attempt < d.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return stagedFile{}, ctx.Err()
			case <-time.After(d.retryDelay):
			}
		}

		sf, err := d.downloadFile(ctx, plan, pf, stagingRoot)
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

func (d *Downloader) downloadFile(ctx context.Context, plan *DownloadPlan, pf PlanFile, stagingRoot string) (stagedFile, error) {
	destName := filepath.Base(pf.Path)
	tmpPath := filepath.Join(stagingRoot, destName+".partial")

	existing, err := os.Stat(tmpPath)
	if err == nil && existing.Size() > 0 && existing.Size() < pf.Size {
		return d.resumeDownload(ctx, plan, pf, tmpPath, existing.Size())
	}

	return d.freshDownload(ctx, plan, pf, tmpPath)
}

func (d *Downloader) freshDownload(ctx context.Context, plan *DownloadPlan, pf PlanFile, tmpPath string) (stagedFile, error) {
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

	if _, err := io.Copy(f, resp.Body); err != nil {
		return stagedFile{}, fmt.Errorf("stream download: %w", err)
	}

	return stagedFile{origPath: pf.Path, tmpPath: tmpPath}, nil
}

func (d *Downloader) resumeDownload(ctx context.Context, plan *DownloadPlan, pf PlanFile, tmpPath string, existingSize int64) (stagedFile, error) {
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
		return d.freshDownload(ctx, plan, pf, tmpPath)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return stagedFile{}, fmt.Errorf("unexpected resume status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return stagedFile{}, fmt.Errorf("open partial for resume: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
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
