package downloader

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dipankardas011/infai/internal/hub"
	"github.com/dipankardas011/infai/internal/model"
)

type DownloadPlan struct {
	RepoID        string
	Revision      string
	EngineKind    model.EngineKind
	Files         []PlanFile
	OptionalFiles []PlanFile
	TotalBytes    int64
}

type PlanFile struct {
	Path   string
	Size   int64
	SHA256 string
}

func ToPlanFiles(entries []hub.FileEntry) []PlanFile {
	files := make([]PlanFile, 0, len(entries))
	for _, e := range entries {
		sha := ""
		if e.LFS != nil {
			sha = e.LFS.SHA256
		}
		files = append(files, PlanFile{
			Path:   e.Path,
			Size:   e.Size,
			SHA256: sha,
		})
	}
	return files
}

// CombinedFiles returns the required files followed by the optional files in
// the order they should be downloaded.
func (p *DownloadPlan) CombinedFiles() []PlanFile {
	files := make([]PlanFile, 0, len(p.Files)+len(p.OptionalFiles))
	files = append(files, p.Files...)
	files = append(files, p.OptionalFiles...)
	return files
}

// CombinedBytes sums the sizes of all required and optional files.
func (p *DownloadPlan) CombinedBytes() int64 {
	return sumBytes(p.CombinedFiles())
}

func ValidatePlan(plan *DownloadPlan) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	if plan.RepoID == "" {
		return fmt.Errorf("repo id is empty")
	}
	if plan.Revision == "" {
		return fmt.Errorf("revision is empty")
	}
	if plan.EngineKind != model.EngineLlamaCPP && plan.EngineKind != model.EngineVLLM {
		return fmt.Errorf("unsupported engine kind %q", plan.EngineKind)
	}
	if len(plan.Files) == 0 {
		return fmt.Errorf("plan has no files")
	}

	for _, f := range plan.CombinedFiles() {
		if strings.Contains(f.Path, "..") {
			return fmt.Errorf("path traversal detected: %q", f.Path)
		}
	}

	seen := make(map[string]bool)
	for _, f := range plan.CombinedFiles() {
		dest := filepath.Base(f.Path)
		if seen[dest] {
			return fmt.Errorf("duplicate destination name: %q", dest)
		}
		seen[dest] = true
	}

	return nil
}
