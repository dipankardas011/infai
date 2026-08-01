package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/mattn/go-sqlite3"

	"github.com/dipankardas011/infai/migrations"
	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/patches"
)

type DB struct {
	conn *sql.DB
}

func Open() (*DB, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfgDir, "infai")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	d := &DB{conn: conn}
	if err := d.runMigrations(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() { d.conn.Close() }

func (d *DB) runMigrations() error {
	m, err := newMigrate(d.conn)
	if err != nil {
		return fmt.Errorf("failed to create migrate: %w", err)
	}

	currentVersion, dirty, err := m.Version()
	switch {
	case err == nil:
	case errors.Is(err, migrate.ErrNilVersion):
		currentVersion = 0
	default:
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	if dirty {
		return fmt.Errorf("database is in dirty state at version %d - manual intervention required", currentVersion)
	}

	err = m.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		if dirty {
			_ = m.Down()
			return fmt.Errorf("migration failed and rolled back: %w", err)
		}
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	newVersion, _, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get new migration version: %w", err)
	}

	if newVersion > currentVersion {
		fmt.Printf("migrated from version %d to %d\n", currentVersion, newVersion)
	}
	if err := patches.Apply(d.conn, int(currentVersion), int(newVersion)); err != nil {
		return fmt.Errorf("patches: %w", err)
	}

	_, err = d.conn.Exec(`
		INSERT OR IGNORE INTO scan_dirs SELECT value FROM settings WHERE key='models_dir' AND value != '';
		DELETE FROM settings WHERE key='models_dir';
	`)
	return err
}

func newMigrate(db *sql.DB) (*migrate.Migrate, error) {
	d, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to create migration source: %w", err)
	}

	driver, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to create database driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", d, "sqlite3", driver)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrate instance: %w", err)
	}

	return m, nil
}

func (d *DB) GetSetting(key string) (string, error) {
	var val string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	return val, err
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(`INSERT OR REPLACE INTO settings VALUES (?, ?)`, key, value)
	return err
}

func (d *DB) ListScanDirs() ([]string, error) {
	rows, err := d.conn.Query(`SELECT path FROM scan_dirs ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) AddScanDir(path string) error {
	_, err := d.conn.Exec(`INSERT OR IGNORE INTO scan_dirs VALUES (?)`, path)
	return err
}

func (d *DB) RemoveScanDir(path string) (err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM models WHERE scan_dir = ?`, path); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM scan_dirs WHERE path = ?`, path); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (d *DB) UpsertModel(m *model.ModelEntry) error {
	ggufPath := m.ModelPath()
	res, err := d.conn.Exec(`
INSERT INTO models (scan_dir, dir_name, model_dir, primary_file, gguf_path, mmproj_path, display_name, type, metadata, source_repo, source_revision, source_files)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gguf_path) DO UPDATE SET
    scan_dir=excluded.scan_dir,
    dir_name=excluded.dir_name,
    model_dir=excluded.model_dir,
    primary_file=excluded.primary_file,
    mmproj_path=excluded.mmproj_path,
    display_name=excluded.display_name,
    type=excluded.type,
    metadata=excluded.metadata,
    source_repo=excluded.source_repo,
    source_revision=excluded.source_revision,
    source_files=excluded.source_files
`, m.ScanDir, m.DirName, m.ModelDir, m.PrimaryFile, ggufPath, m.MmprojPath, m.DisplayName, m.Type, m.Metadata, m.SourceRepo, m.SourceRevision, m.SourceFiles)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil && id > 0 {
		m.ID = id
	} else {
		err = d.conn.QueryRow(`SELECT id FROM models WHERE gguf_path = ?`, ggufPath).Scan(&m.ID)
	}
	return err
}

func (d *DB) ListRecents(limit int) ([]RecentEntry, error) {
	rows, err := d.conn.Query(`
SELECT m.id, m.scan_dir, m.dir_name, m.model_dir, m.primary_file, m.mmproj_path, m.display_name, m.type, m.metadata, m.source_repo, m.source_revision, m.source_files,
       ie.id, ie.name, ie.path, ie.kind, ie.base_args, ie.environment,
       p.id, p.model_id, p.inference_engine_id, p.name, p.port, p.host, p.context_size, p.ngl,
       p.batch_size, p.ubatch_size, p.cache_type_k, p.cache_type_v,
       p.flash_attn, p.jinja, p.temperature, p.reasoning_budget, p.top_p, p.top_k,
       p.no_kv_offload, p.use_mmproj, p.extra_flags, p.engine_config
FROM recents r
JOIN models m ON r.model_id = m.id
JOIN profiles p ON r.profile_id = p.id
JOIN inference_engine ie ON p.inference_engine_id = ie.id
ORDER BY r.last_used DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecentEntry
	for rows.Next() {
		var m model.ModelEntry
		var ie model.InferenceEngine
		var p model.Profile
		var baseArgs, environment string
		var flashAttn, jinja, noKVOffload, useMmproj int
		err := rows.Scan(
			&m.ID, &m.ScanDir, &m.DirName, &m.ModelDir, &m.PrimaryFile, &m.MmprojPath, &m.DisplayName, &m.Type, &m.Metadata, &m.SourceRepo, &m.SourceRevision, &m.SourceFiles,
			&ie.ID, &ie.Name, &ie.Path, &ie.Kind, &baseArgs, &environment,
			&p.ID, &p.ModelID, &p.InferenceEngineID, &p.Name, &p.Port, &p.Host, &p.ContextSize, &p.NGL,
			&p.BatchSize, &p.UBatchSize, &p.CacheTypeK, &p.CacheTypeV,
			&flashAttn, &jinja, &p.Temperature, &p.ReasoningBudget, &p.TopP, &p.TopK,
			&noKVOffload, &useMmproj, &p.ExtraFlags, &p.EngineConfig,
		)
		if err != nil {
			return nil, err
		}
		if err := decodeEngineRuntime(&ie, baseArgs, environment); err != nil {
			return nil, err
		}
		p.FlashAttn = flashAttn == 1
		p.Jinja = jinja == 1
		p.NoKVOffload = noKVOffload == 1
		p.UseMmproj = useMmproj == 1
		out = append(out, RecentEntry{Model: m, InferenceEngine: ie, Profile: p})
	}
	return out, rows.Err()
}

func (d *DB) Sync(scanned []model.ModelEntry) (int, int, error) {
	byRoot := make(map[string][]model.ModelEntry)
	for _, e := range scanned {
		byRoot[e.ScanDir] = append(byRoot[e.ScanDir], e)
	}
	return d.SyncPerRoot(byRoot)
}

func (d *DB) SyncPerRoot(scannedByRoot map[string][]model.ModelEntry) (int, int, error) {
	successfulRoots := make(map[string]bool, len(scannedByRoot))
	for root := range scannedByRoot {
		successfulRoots[root] = true
	}

	scannedSet := make(map[string]bool)
	for _, entries := range scannedByRoot {
		for _, m := range entries {
			scannedSet[key(m.ModelDir, m.PrimaryFile)] = true
		}
	}

	var removed, updated int

	tx, err := d.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	existing, err := tx.Query(`SELECT id, scan_dir, model_dir, primary_file FROM models`)
	if err != nil {
		return 0, 0, err
	}

	if existing.Err() != nil {
		return 0, 0, existing.Err()
	}

	type dbModel struct {
		id          int64
		scanDir     string
		modelDir    string
		primaryFile string
	}
	var dbModels []dbModel
	for existing.Next() {
		var dm dbModel
		if err := existing.Scan(&dm.id, &dm.scanDir, &dm.modelDir, &dm.primaryFile); err != nil {
			existing.Close()
			return 0, 0, err
		}
		dbModels = append(dbModels, dm)
	}
	existing.Close()

	for _, dm := range dbModels {
		artifactPath := dm.modelDir
		if dm.primaryFile != "" {
			artifactPath = filepath.Join(dm.modelDir, dm.primaryFile)
		}
		// Always remove models whose files no longer exist on disk.
		if _, statErr := os.Stat(artifactPath); os.IsNotExist(statErr) {
			_, err := tx.Exec(`DELETE FROM models WHERE id = ?`, dm.id)
			if err != nil {
				return 0, 0, err
			}
			_, err = tx.Exec(`DELETE FROM recents WHERE model_id = ?`, dm.id)
			if err != nil {
				return 0, 0, err
			}
			removed++
			continue
		}

		// If the model's scan root was scanned successfully but the model
		// is no longer in the scanned set, remove it. If the root wasn't
		// scanned (error or not in this sync), preserve the model.
		if successfulRoots[dm.scanDir] && !scannedSet[key(dm.modelDir, dm.primaryFile)] {
			_, err := tx.Exec(`DELETE FROM models WHERE id = ?`, dm.id)
			if err != nil {
				return 0, 0, err
			}
			_, err = tx.Exec(`DELETE FROM recents WHERE model_id = ?`, dm.id)
			if err != nil {
				return 0, 0, err
			}
			removed++
		}
	}

	seen := make(map[string]bool)
	for _, entries := range scannedByRoot {
		for _, m := range entries {
			k := key(m.ModelDir, m.PrimaryFile)
			if seen[k] {
				continue
			}
			seen[k] = true
			ggufPath := m.ModelPath()
			_, err := tx.Exec(`
			INSERT INTO models (scan_dir, dir_name, model_dir, primary_file, gguf_path, mmproj_path, display_name, type, metadata, source_repo, source_revision, source_files)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(gguf_path) DO UPDATE SET
				scan_dir=excluded.scan_dir,
				dir_name=excluded.dir_name,
				model_dir=excluded.model_dir,
				primary_file=excluded.primary_file,
				mmproj_path=excluded.mmproj_path,
				display_name=excluded.display_name,
				type=excluded.type,
				metadata=excluded.metadata,
				source_repo=excluded.source_repo,
				source_revision=excluded.source_revision,
				source_files=excluded.source_files,
				last_verified=CURRENT_TIMESTAMP
		`, m.ScanDir, m.DirName, m.ModelDir, m.PrimaryFile, ggufPath, m.MmprojPath, m.DisplayName, m.Type, m.Metadata, m.SourceRepo, m.SourceRevision, m.SourceFiles)
			if err != nil {
				return 0, 0, err
			}
			updated++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	return removed, updated, nil
}

func key(modelDir, primaryFile string) string {
	return modelDir + "\x00" + primaryFile
}

func (d *DB) ListModels() ([]model.ModelEntry, error) {
	rows, err := d.conn.Query(`SELECT id, scan_dir, dir_name, model_dir, primary_file, mmproj_path, display_name, type, metadata, source_repo, source_revision, source_files FROM models ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelEntry
	for rows.Next() {
		var m model.ModelEntry
		if err := rows.Scan(&m.ID, &m.ScanDir, &m.DirName, &m.ModelDir, &m.PrimaryFile, &m.MmprojPath, &m.DisplayName, &m.Type, &m.Metadata, &m.SourceRepo, &m.SourceRevision, &m.SourceFiles); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) ListRecentModels(limit int) ([]model.ModelEntry, error) {
	rows, err := d.conn.Query(`SELECT id, scan_dir, dir_name, model_dir, primary_file, mmproj_path, display_name, type, metadata, source_repo, source_revision, source_files FROM models ORDER BY last_used DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelEntry
	for rows.Next() {
		var m model.ModelEntry
		if err := rows.Scan(&m.ID, &m.ScanDir, &m.DirName, &m.ModelDir, &m.PrimaryFile, &m.MmprojPath, &m.DisplayName, &m.Type, &m.Metadata, &m.SourceRepo, &m.SourceRevision, &m.SourceFiles); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) MarkModelUsed(id int64) error {
	_, err := d.conn.Exec(`UPDATE models SET last_used = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (d *DB) MarkRecent(modelID, profileID int64) error {
	_, err := d.conn.Exec(`
INSERT INTO recents (model_id, profile_id, last_used)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(model_id, profile_id) DO UPDATE SET last_used=excluded.last_used
`, modelID, profileID)
	return err
}

func (d *DB) CreateInferenceEngine(e model.InferenceEngine) error {
	baseArgs, err := json.Marshal(e.BaseArgs)
	if err != nil {
		return err
	}
	environment, err := json.Marshal(e.Env)
	if err != nil {
		return err
	}
	if e.Kind == "" {
		e.Kind = model.EngineLlamaCPP
	}
	_, err = d.conn.Exec(`INSERT INTO inference_engine (id, name, path, kind, base_args, environment) VALUES (?, ?, ?, ?, ?, ?)`, e.ID, e.Name, e.Path, e.Kind, baseArgs, environment)
	return err
}

func (d *DB) UpdateInferenceEngineName(id, name string) error {
	_, err := d.conn.Exec(`UPDATE inference_engine SET name = ? WHERE id = ?`, name, id)
	return err
}

func (d *DB) UpdateInferenceEnginePath(id, path string) error {
	_, err := d.conn.Exec(`UPDATE inference_engine SET path = ? WHERE id = ?`, path, id)
	return err
}

func (d *DB) GetInferenceEngineByID(id string) (model.InferenceEngine, error) {
	var e model.InferenceEngine
	var baseArgs, environment string
	err := d.conn.QueryRow(`SELECT id, name, path, kind, base_args, environment FROM inference_engine WHERE id = ?`, id).Scan(&e.ID, &e.Name, &e.Path, &e.Kind, &baseArgs, &environment)
	if err == nil {
		err = decodeEngineRuntime(&e, baseArgs, environment)
	}
	return e, err
}

func (d *DB) ListInferenceEngines() ([]model.InferenceEngine, error) {
	rows, err := d.conn.Query(`SELECT id, name, path, kind, base_args, environment FROM inference_engine ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.InferenceEngine
	for rows.Next() {
		var e model.InferenceEngine
		var baseArgs, environment string
		if err := rows.Scan(&e.ID, &e.Name, &e.Path, &e.Kind, &baseArgs, &environment); err != nil {
			return nil, err
		}
		if err := decodeEngineRuntime(&e, baseArgs, environment); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func decodeEngineRuntime(e *model.InferenceEngine, baseArgs, environment string) error {
	if strings.TrimSpace(baseArgs) == "" {
		baseArgs = "[]"
	}
	if strings.TrimSpace(environment) == "" {
		environment = "{}"
	}
	if err := json.Unmarshal([]byte(baseArgs), &e.BaseArgs); err != nil {
		return fmt.Errorf("decode inference engine base args: %w", err)
	}
	if err := json.Unmarshal([]byte(environment), &e.Env); err != nil {
		return fmt.Errorf("decode inference engine environment: %w", err)
	}
	return nil
}

func (d *DB) DeleteInferenceEngine(id string) (err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM profiles WHERE inference_engine_id = ?`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM inference_engine WHERE id = ?`, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type RecentEntry struct {
	Model           model.ModelEntry
	InferenceEngine model.InferenceEngine
	Profile         model.Profile
}

type ProfileEntry struct {
	Model           model.ModelEntry
	InferenceEngine model.InferenceEngine
	Profile         model.Profile
}

func (d *DB) ListAllProfiles() ([]ProfileEntry, error) {
	rows, err := d.conn.Query(`
SELECT m.id, m.scan_dir, m.dir_name, m.model_dir, m.primary_file, m.mmproj_path, m.display_name, m.type, m.metadata, m.source_repo, m.source_revision, m.source_files,
       ie.id, ie.name, ie.path, ie.kind, ie.base_args, ie.environment,
       p.id, p.model_id, p.inference_engine_id, p.name, p.port, p.host, p.context_size, p.ngl,
       p.batch_size, p.ubatch_size, p.cache_type_k, p.cache_type_v,
       p.flash_attn, p.jinja, p.temperature, p.reasoning_budget, p.top_p, p.top_k,
       p.no_kv_offload, p.use_mmproj, p.extra_flags, p.engine_config
FROM profiles p
JOIN models m ON p.model_id = m.id
JOIN inference_engine ie ON p.inference_engine_id = ie.id
ORDER BY lower(m.display_name), lower(ie.name), lower(p.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProfileEntry
	for rows.Next() {
		var m model.ModelEntry
		var ie model.InferenceEngine
		var p model.Profile
		var baseArgs, environment string
		var flashAttn, jinja, noKVOffload, useMmproj int
		err := rows.Scan(
			&m.ID, &m.ScanDir, &m.DirName, &m.ModelDir, &m.PrimaryFile, &m.MmprojPath, &m.DisplayName, &m.Type, &m.Metadata, &m.SourceRepo, &m.SourceRevision, &m.SourceFiles,
			&ie.ID, &ie.Name, &ie.Path, &ie.Kind, &baseArgs, &environment,
			&p.ID, &p.ModelID, &p.InferenceEngineID, &p.Name, &p.Port, &p.Host, &p.ContextSize, &p.NGL,
			&p.BatchSize, &p.UBatchSize, &p.CacheTypeK, &p.CacheTypeV,
			&flashAttn, &jinja, &p.Temperature, &p.ReasoningBudget, &p.TopP, &p.TopK,
			&noKVOffload, &useMmproj, &p.ExtraFlags, &p.EngineConfig,
		)
		if err != nil {
			return nil, err
		}
		if err := decodeEngineRuntime(&ie, baseArgs, environment); err != nil {
			return nil, err
		}
		p.FlashAttn = flashAttn == 1
		p.Jinja = jinja == 1
		p.NoKVOffload = noKVOffload == 1
		p.UseMmproj = useMmproj == 1
		out = append(out, ProfileEntry{Model: m, InferenceEngine: ie, Profile: p})
	}
	return out, rows.Err()
}

func (d *DB) ListProfiles(modelID int64) ([]model.Profile, error) {
	rows, err := d.conn.Query(`
SELECT id, model_id, inference_engine_id, name, port, host, context_size, ngl,
       batch_size, ubatch_size, cache_type_k, cache_type_v,
       flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
       no_kv_offload, use_mmproj, extra_flags, engine_config
FROM profiles WHERE model_id = ? ORDER BY name`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) GetProfile(id int64) (model.Profile, error) {
	rows, err := d.conn.Query(`
SELECT id, model_id, inference_engine_id, name, port, host, context_size, ngl,
       batch_size, ubatch_size, cache_type_k, cache_type_v,
       flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
       no_kv_offload, use_mmproj, extra_flags, engine_config
FROM profiles WHERE id = ?`, id)
	if err != nil {
		return model.Profile{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return model.Profile{}, sql.ErrNoRows
	}
	p, err := scanProfile(rows)
	if err != nil {
		return model.Profile{}, err
	}
	return p, rows.Err()
}

func (d *DB) UpsertProfile(p *model.Profile) error {
	res, err := d.conn.Exec(`
INSERT INTO profiles (model_id, inference_engine_id, name, port, host, context_size, ngl,
    batch_size, ubatch_size, cache_type_k, cache_type_v,
    flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
    no_kv_offload, use_mmproj, extra_flags, engine_config)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(model_id, name) DO UPDATE SET
    inference_engine_id=excluded.inference_engine_id,
    port=excluded.port, host=excluded.host, context_size=excluded.context_size,
    ngl=excluded.ngl, batch_size=excluded.batch_size, ubatch_size=excluded.ubatch_size,
    cache_type_k=excluded.cache_type_k, cache_type_v=excluded.cache_type_v,
    flash_attn=excluded.flash_attn, jinja=excluded.jinja,
    temperature=excluded.temperature, reasoning_budget=excluded.reasoning_budget,
    top_p=excluded.top_p, top_k=excluded.top_k,
    no_kv_offload=excluded.no_kv_offload, use_mmproj=excluded.use_mmproj,
    extra_flags=excluded.extra_flags, engine_config=excluded.engine_config
`, p.ModelID, p.InferenceEngineID, p.Name, p.Port, p.Host, p.ContextSize, p.NGL,
		p.BatchSize, p.UBatchSize, p.CacheTypeK, p.CacheTypeV,
		boolToInt(p.FlashAttn), boolToInt(p.Jinja),
		p.Temperature, p.ReasoningBudget, p.TopP, p.TopK,
		boolToInt(p.NoKVOffload), boolToInt(p.UseMmproj), p.ExtraFlags, string(p.EngineConfig))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil && id > 0 {
		p.ID = id
	} else {
		err = d.conn.QueryRow(`SELECT id FROM profiles WHERE model_id=? AND name=?`, p.ModelID, p.Name).Scan(&p.ID)
	}
	return err
}

func (d *DB) DeleteProfile(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	return err
}

func scanProfile(rows *sql.Rows) (model.Profile, error) {
	var p model.Profile
	var flashAttn, jinja, noKVOffload, useMmproj int
	err := rows.Scan(
		&p.ID, &p.ModelID, &p.InferenceEngineID, &p.Name, &p.Port, &p.Host, &p.ContextSize, &p.NGL,
		&p.BatchSize, &p.UBatchSize, &p.CacheTypeK, &p.CacheTypeV,
		&flashAttn, &jinja, &p.Temperature, &p.ReasoningBudget, &p.TopP, &p.TopK,
		&noKVOffload, &useMmproj, &p.ExtraFlags, &p.EngineConfig,
	)
	p.FlashAttn = flashAttn == 1
	p.Jinja = jinja == 1
	p.NoKVOffload = noKVOffload == 1
	p.UseMmproj = useMmproj == 1
	return p, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
