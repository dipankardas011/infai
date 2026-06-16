package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

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
	ScanDirs         []string
	Models           []model.ModelEntry
	Recents          []db.RecentEntry
	Profiles         []db.ProfileEntry
	InferenceEngines []model.InferenceEngine
}

func (s *Service) LoadHomeData() (HomeData, error) {
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
	inferenceEngines, err := s.db.ListInferenceEngines()
	if err != nil {
		errs = append(errs, fmt.Errorf("inference engines: %w", err))
	}

	data.ScanDirs = scanDirs
	data.Models = models
	data.Recents = recents
	data.Profiles = profiles
	data.InferenceEngines = inferenceEngines
	return data, errors.Join(errs...)
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
	if p.InferenceEngineID == "" {
		return fmt.Errorf("inference engine is required")
	}
	if _, err := s.db.GetInferenceEngineByID(p.InferenceEngineID); err != nil {
		return fmt.Errorf("inference engine: %w", err)
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

func (s *Service) BuildLaunchArgsWithPort(m model.ModelEntry, p model.Profile, port int) ([]string, error) {
	p.Port = port
	return s.BuildLaunchArgs(m, p)
}

func (s *Service) BuildLaunchArgs(m model.ModelEntry, p model.Profile) ([]string, error) {
	if m.ID <= 0 {
		return nil, fmt.Errorf("invalid model")
	}
	if p.ID <= 0 {
		return nil, fmt.Errorf("invalid profile")
	}
	if p.InferenceEngineID == "" {
		return nil, fmt.Errorf("profile has no inference engine")
	}
	engine, err := s.db.GetInferenceEngineByID(p.InferenceEngineID)
	if err != nil {
		return nil, fmt.Errorf("inference engine: %w", err)
	}
	engineBin, err := resolveInferenceEngineBinary(engine.Path)
	if err != nil {
		return nil, err
	}
	args := launcher.BuildArgs(engineBin, m, p)
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("failed to build launch args")
	}
	return args, nil
}

func resolveInferenceEngineBinary(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("inference engine path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inference engine path: %w", err)
	}
	if !info.IsDir() {
		return path, nil
	}
	bin := "llama-server"

	return filepath.Join(path, bin), nil
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

func (s *Service) CreateInferenceEngine(name, path string) (model.InferenceEngine, error) {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" {
		return model.InferenceEngine{}, fmt.Errorf("inference engine name is empty")
	}
	if path == "" {
		return model.InferenceEngine{}, fmt.Errorf("inference engine path is empty")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return model.InferenceEngine{}, err
	}
	engine := model.InferenceEngine{ID: id.String(), Name: name, Path: path}
	if err := s.db.CreateInferenceEngine(engine); err != nil {
		return model.InferenceEngine{}, err
	}
	return engine, nil
}

func (s *Service) UpdateInferenceEngineName(id, name string) error {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return fmt.Errorf("inference engine id is empty")
	}
	if name == "" {
		return fmt.Errorf("inference engine name is empty")
	}
	return s.db.UpdateInferenceEngineName(id, name)
}

func (s *Service) UpdateInferenceEnginePath(id, path string) error {
	id = strings.TrimSpace(id)
	path = strings.TrimSpace(path)
	if id == "" {
		return fmt.Errorf("inference engine id is empty")
	}
	if path == "" {
		return fmt.Errorf("inference engine path is empty")
	}
	return s.db.UpdateInferenceEnginePath(id, path)
}

func (s *Service) GetInferenceEngineByID(id string) (model.InferenceEngine, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return model.InferenceEngine{}, fmt.Errorf("inference engine id is empty")
	}
	return s.db.GetInferenceEngineByID(id)
}

func (s *Service) ListInferenceEngines() ([]model.InferenceEngine, error) {
	return s.db.ListInferenceEngines()
}

func (s *Service) DeleteInferenceEngine(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("inference engine id is empty")
	}
	return s.db.DeleteInferenceEngine(id)
}
