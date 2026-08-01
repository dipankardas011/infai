package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/inference"
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

	var m *model.ModelEntry
	var engine *model.InferenceEngine

	if p.ModelID > 0 {
		models, err := s.db.ListModels()
		if err == nil {
			for i := range models {
				if models[i].ID == p.ModelID {
					m = &models[i]
					break
				}
			}
		}
	}
	if p.InferenceEngineID != "" {
		e, err := s.db.GetInferenceEngineByID(p.InferenceEngineID)
		if err == nil {
			engine = &e
		}
	}

	issues := ValidateProfile(p, m, engine)
	if issues.HasErrors() {
		return fmt.Errorf("validation: %w", issues)
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

func (s *Service) BuildRunSpec(m model.ModelEntry, p model.Profile, port int) (inference.RunSpec, error) {
	if m.ID <= 0 {
		return inference.RunSpec{}, fmt.Errorf("invalid model")
	}
	if p.ID <= 0 {
		return inference.RunSpec{}, fmt.Errorf("invalid profile")
	}
	if p.InferenceEngineID == "" {
		return inference.RunSpec{}, fmt.Errorf("profile has no inference engine")
	}
	p.Port = port
	engine, err := s.db.GetInferenceEngineByID(p.InferenceEngineID)
	if err != nil {
		return inference.RunSpec{}, fmt.Errorf("inference engine: %w", err)
	}
	engineBin, err := resolveInferenceEngineBinary(engine.Path, engine.Kind)
	if err != nil {
		return inference.RunSpec{}, err
	}
	engine.Path = engineBin
	adapter, err := inference.AdapterFor(engine.Kind)
	if err != nil {
		return inference.RunSpec{}, err
	}
	launch, err := adapter.BuildLaunchSpec(engine, m, p)
	if err != nil {
		return inference.RunSpec{}, err
	}
	return inference.RunSpec{Launch: launch, Metrics: adapter.NewMetricsSource(p.Host, p.Port)}, nil
}

func resolveInferenceEngineBinary(path string, kind model.EngineKind) (string, error) {
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
	if kind == model.EngineVLLM {
		bin = "vllm"
	}

	return filepath.Join(path, bin), nil
}

type SyncResult struct {
	Removed int
	Updated int
	Models  []model.ModelEntry
	Issues  []scanner.ScanResult
}

func (s *Service) SyncModels(folders []string) (SyncResult, error) {
	return s.SyncModelsWithContext(context.Background(), folders, nil)
}

func (s *Service) SyncModelsWithContext(ctx context.Context, folders []string, progress scanner.ProgressFunc) (SyncResult, error) {
	if len(folders) == 0 {
		return SyncResult{}, fmt.Errorf("no scan folders configured")
	}

	results := scanner.ScanWithContext(ctx, folders, progress)
	scannedByRoot := make(map[string][]model.ModelEntry)
	var issues []scanner.ScanResult

	for _, r := range results {
		if r.Error != nil {
			issues = append(issues, r)
			continue
		}
		entriesWithMeta := make([]model.ModelEntry, 0, len(r.Entries))
		for _, entry := range r.Entries {
			e := entry
			if err := scanner.LoadModelMetadata(&e); err != nil {
				issues = append(issues, scanner.ScanResult{
					RootDir: r.RootDir,
					Error:   fmt.Errorf("metadata parse for %s: %w", e.DisplayName, err),
				})
				continue
			}
			entriesWithMeta = append(entriesWithMeta, e)
		}
		scannedByRoot[r.RootDir] = entriesWithMeta
	}

	removed, updated, err := s.db.SyncPerRoot(scannedByRoot)
	if err != nil {
		return SyncResult{}, fmt.Errorf("sync: %w", err)
	}

	models, err := s.db.ListModels()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list models after sync: %w", err)
	}

	return SyncResult{Removed: removed, Updated: updated, Models: models, Issues: issues}, nil
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
	return s.CreateInferenceEngineConfig(name, path, model.EngineLlamaCPP, nil, nil)
}

func (s *Service) CreateInferenceEngineConfig(name, path string, kind model.EngineKind, baseArgs []string, env map[string]string) (model.InferenceEngine, error) {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" {
		return model.InferenceEngine{}, fmt.Errorf("inference engine name is empty")
	}
	if path == "" {
		return model.InferenceEngine{}, fmt.Errorf("inference engine path is empty")
	}
	if kind != model.EngineLlamaCPP && kind != model.EngineVLLM {
		return model.InferenceEngine{}, fmt.Errorf("unsupported inference engine kind %q", kind)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return model.InferenceEngine{}, err
	}
	engine := model.InferenceEngine{ID: id.String(), Name: name, Kind: kind, Path: path, BaseArgs: baseArgs, Env: env}
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
