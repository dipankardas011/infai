package backend

import (
	"database/sql"
	"errors"
	"fmt"
	"os/exec"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/launcher"
	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/scanner"
)

// Service is the application/use-case layer. It is intentionally UI-free.
// TUI screens should call this layer and decide how to present results/errors.
type Service struct {
	db *db.DB
}

func New(database *db.DB) *Service {
	return &Service{db: database}
}

func (s *Service) GetSetting(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("setting key is empty")
	}
	return s.db.GetSetting(key)
}

func (s *Service) SetSetting(key, value string) error {
	if key == "" {
		return fmt.Errorf("setting key is empty")
	}
	return s.db.SetSetting(key, value)
}

type HomeData struct {
	ScanDirs  []string
	Models    []model.ModelEntry
	Recents   []db.RecentEntry
	Profiles  []db.ProfileEntry
	ServerBin string
}

func (s *Service) LoadHomeData(serverBin string) (HomeData, error) {
	var data HomeData
	var errs []error

	scanDirs, err := s.db.ListScanDirs()
	if err != nil {
		errs = append(errs, fmt.Errorf("scan dirs: %w", err))
	}
	models, err := s.db.ListModels()
	if err != nil {
		errs = append(errs, fmt.Errorf("models: %w", err))
	}
	recents, err := s.db.ListRecents(3)
	if err != nil {
		errs = append(errs, fmt.Errorf("recents: %w", err))
	}
	profiles, err := s.db.ListAllProfiles()
	if err != nil {
		errs = append(errs, fmt.Errorf("profiles: %w", err))
	}
	resolvedBin, err := s.DefaultExecutorPath(serverBin)
	if err != nil {
		errs = append(errs, fmt.Errorf("executor: %w", err))
	} else {
		serverBin = resolvedBin
	}

	data.ScanDirs = scanDirs
	data.Models = models
	data.Recents = recents
	data.Profiles = profiles
	data.ServerBin = serverBin
	return data, errors.Join(errs...)
}

func (s *Service) DefaultExecutorPath(fallback string) (string, error) {
	path, err := s.db.GetDefaultExecutorPath()
	if err == nil && path != "" {
		return path, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if fallback != "" {
		return fallback, nil
	}
	if detected, err := exec.LookPath("llama-server"); err == nil {
		return detected, nil
	}
	return "", nil
}

func (s *Service) ListModels() ([]model.ModelEntry, error) {
	return s.db.ListModels()
}

func (s *Service) GetProfile(id int64) (model.Profile, error) {
	return s.db.GetProfile(id)
}

func (s *Service) SaveProfile(p *model.Profile) error {
	if p == nil {
		return fmt.Errorf("profile is nil")
	}
	return s.db.UpsertProfile(p)
}

func (s *Service) DeleteProfile(id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid profile id")
	}
	return s.db.DeleteProfile(id)
}

func (s *Service) MarkRecent(modelID, profileID int64) error {
	if modelID <= 0 || profileID <= 0 {
		return fmt.Errorf("invalid recent ids")
	}
	return s.db.MarkRecent(modelID, profileID)
}

func (s *Service) BuildLaunchArgs(serverBin string, m model.ModelEntry, p model.Profile) ([]string, error) {
	if serverBin == "" {
		return nil, fmt.Errorf("executor path is empty")
	}
	if m.ID <= 0 {
		return nil, fmt.Errorf("invalid model")
	}
	if p.ID <= 0 {
		return nil, fmt.Errorf("invalid profile")
	}
	args := launcher.BuildArgs(serverBin, m, p)
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("failed to build launch args")
	}
	return args, nil
}

type SyncResult struct {
	Removed int
	Updated int
	Models  []model.ModelEntry
}

func (s *Service) SyncModels(folders []string) (SyncResult, error) {
	if len(folders) == 0 {
		return SyncResult{}, fmt.Errorf("no scan folders configured")
	}
	entries, err := scanner.Scan(folders)
	if err != nil {
		return SyncResult{}, fmt.Errorf("scan: %w", err)
	}
	for i := range entries {
		if err := scanner.LoadModelMetadata(s.db, &entries[i]); err != nil {
			return SyncResult{}, fmt.Errorf("load metadata: %w", err)
		}
	}
	removed, updated, err := s.db.Sync(entries)
	if err != nil {
		return SyncResult{}, fmt.Errorf("sync: %w", err)
	}
	models, err := s.db.ListModels()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list models after sync: %w", err)
	}
	return SyncResult{Removed: removed, Updated: updated, Models: models}, nil
}

func (s *Service) AddScanDir(path string) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	return s.db.AddScanDir(path)
}

func (s *Service) RemoveScanDir(path string) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	return s.db.RemoveScanDir(path)
}

func (s *Service) ListExecutors() ([]db.Executor, error) {
	return s.db.ListExecutors()
}

func (s *Service) SaveExecutor(e db.Executor) error {
	if e.ID == "" {
		return fmt.Errorf("executor id is empty")
	}
	if e.Path == "" {
		return fmt.Errorf("executor path is empty")
	}
	return s.db.UpsertExecutor(e)
}

func (s *Service) SetDefaultExecutor(id string) error {
	if id == "" {
		return fmt.Errorf("executor id is empty")
	}
	return s.db.SetDefaultExecutor(id)
}
