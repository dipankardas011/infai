package db

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"

	"github.com/dipankardas011/infai/model"
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
	if err := d.migrate(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() { d.conn.Close() }

func (d *DB) migrate() error {
	// Add columns to models for existing databases — ignore error if already exists.
	d.conn.Exec(`ALTER TABLE models ADD COLUMN scan_dir TEXT NOT NULL DEFAULT ''`)
	d.conn.Exec(`ALTER TABLE models ADD COLUMN last_used DATETIME DEFAULT CURRENT_TIMESTAMP`)

	_, err := d.conn.Exec(`
CREATE TABLE IF NOT EXISTS models (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_dir     TEXT NOT NULL DEFAULT '',
    dir_name     TEXT NOT NULL,
    gguf_path    TEXT NOT NULL UNIQUE,
    mmproj_path  TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    last_used    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scan_dirs (
    path TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS profiles (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id         INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    port             INTEGER NOT NULL DEFAULT 8000,
    host             TEXT NOT NULL DEFAULT '0.0.0.0',
    context_size     INTEGER NOT NULL DEFAULT 65536,
    ngl              TEXT NOT NULL DEFAULT 'auto',
    batch_size       INTEGER,
    ubatch_size      INTEGER,
    cache_type_k     TEXT,
    cache_type_v     TEXT,
    flash_attn       INTEGER NOT NULL DEFAULT 0,
    jinja            INTEGER NOT NULL DEFAULT 0,
    temperature      REAL,
    reasoning_budget INTEGER,
    top_p            REAL,
    top_k            INTEGER,
    no_kv_offload    INTEGER NOT NULL DEFAULT 0,
    use_mmproj       INTEGER NOT NULL DEFAULT 0,
    extra_flags      TEXT NOT NULL DEFAULT '',
    UNIQUE(model_id, name)
);

CREATE TABLE IF NOT EXISTS recents (
    model_id   INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    last_used  DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (model_id, profile_id)
);

CREATE TABLE IF NOT EXISTS executors (
    id              TEXT PRIMARY KEY,
    path            TEXT NOT NULL,
    is_default      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO settings VALUES ('theme', 'gruvbox');

-- migrate legacy models_dir into scan_dirs exactly once, then drop it
INSERT OR IGNORE INTO scan_dirs SELECT value FROM settings WHERE key='models_dir' AND value != '';
DELETE FROM settings WHERE key='models_dir';
`)
	return err
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

func (d *DB) RemoveScanDir(path string) error {
	_, err := d.conn.Exec(`DELETE FROM scan_dirs WHERE path = ?`, path)
	return err
}

func (d *DB) UpsertModel(m *model.ModelEntry) error {
	res, err := d.conn.Exec(`
INSERT INTO models (scan_dir, dir_name, gguf_path, mmproj_path, display_name)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(gguf_path) DO UPDATE SET
    scan_dir=excluded.scan_dir,
    dir_name=excluded.dir_name,
    mmproj_path=excluded.mmproj_path,
    display_name=excluded.display_name
`, m.ScanDir, m.DirName, m.GGUFPath, m.MmprojPath, m.DisplayName)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil && id > 0 {
		m.ID = id
	} else {
		err = d.conn.QueryRow(`SELECT id FROM models WHERE gguf_path = ?`, m.GGUFPath).Scan(&m.ID)
	}
	return err
}

func (d *DB) ListModels() ([]model.ModelEntry, error) {
	rows, err := d.conn.Query(`SELECT id, scan_dir, dir_name, gguf_path, mmproj_path, display_name FROM models ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelEntry
	for rows.Next() {
		var m model.ModelEntry
		if err := rows.Scan(&m.ID, &m.ScanDir, &m.DirName, &m.GGUFPath, &m.MmprojPath, &m.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) ListRecentModels(limit int) ([]model.ModelEntry, error) {
	rows, err := d.conn.Query(`SELECT id, scan_dir, dir_name, gguf_path, mmproj_path, display_name FROM models ORDER BY last_used DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelEntry
	for rows.Next() {
		var m model.ModelEntry
		if err := rows.Scan(&m.ID, &m.ScanDir, &m.DirName, &m.GGUFPath, &m.MmprojPath, &m.DisplayName); err != nil {
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

type Executor struct {
	ID        string
	Path      string
	IsDefault bool
}

func (d *DB) ListExecutors() ([]Executor, error) {
	rows, err := d.conn.Query(`SELECT id, path, is_default FROM executors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Executor
	for rows.Next() {
		var e Executor
		var isDefault int
		if err := rows.Scan(&e.ID, &e.Path, &isDefault); err != nil {
			return nil, err
		}
		e.IsDefault = isDefault == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) UpsertExecutor(e Executor) error {
	_, err := d.conn.Exec(`
INSERT INTO executors (id, path, is_default)
VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET path=excluded.path, is_default=excluded.is_default
`, e.ID, e.Path, boolToInt(e.IsDefault))
	return err
}

func (d *DB) SetDefaultExecutor(id string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE executors SET is_default = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE executors SET is_default = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) GetDefaultExecutorPath() (string, error) {
	var path string
	err := d.conn.QueryRow(`SELECT path FROM executors WHERE is_default = 1`).Scan(&path)
	return path, err
}

type RecentEntry struct {
	Model   model.ModelEntry
	Profile model.Profile
}

func (d *DB) ListRecents(limit int) ([]RecentEntry, error) {
	rows, err := d.conn.Query(`
SELECT m.id, m.scan_dir, m.dir_name, m.gguf_path, m.mmproj_path, m.display_name,
       p.id, p.model_id, p.name, p.port, p.host, p.context_size, p.ngl,
       p.batch_size, p.ubatch_size, p.cache_type_k, p.cache_type_v,
       p.flash_attn, p.jinja, p.temperature, p.reasoning_budget, p.top_p, p.top_k,
       p.no_kv_offload, p.use_mmproj, p.extra_flags
FROM recents r
JOIN models m ON r.model_id = m.id
JOIN profiles p ON r.profile_id = p.id
ORDER BY r.last_used DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecentEntry
	for rows.Next() {
		var m model.ModelEntry
		var p model.Profile
		var flashAttn, jinja, noKVOffload, useMmproj int
		err := rows.Scan(
			&m.ID, &m.ScanDir, &m.DirName, &m.GGUFPath, &m.MmprojPath, &m.DisplayName,
			&p.ID, &p.ModelID, &p.Name, &p.Port, &p.Host, &p.ContextSize, &p.NGL,
			&p.BatchSize, &p.UBatchSize, &p.CacheTypeK, &p.CacheTypeV,
			&flashAttn, &jinja, &p.Temperature, &p.ReasoningBudget, &p.TopP, &p.TopK,
			&noKVOffload, &useMmproj, &p.ExtraFlags,
		)
		if err != nil {
			return nil, err
		}
		p.FlashAttn = flashAttn == 1
		p.Jinja = jinja == 1
		p.NoKVOffload = noKVOffload == 1
		p.UseMmproj = useMmproj == 1
		out = append(out, RecentEntry{Model: m, Profile: p})
	}
	return out, rows.Err()
}

func (d *DB) ListProfiles(modelID int64) ([]model.Profile, error) {
	rows, err := d.conn.Query(`
SELECT id, model_id, name, port, host, context_size, ngl,
       batch_size, ubatch_size, cache_type_k, cache_type_v,
       flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
       no_kv_offload, use_mmproj, extra_flags
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

func (d *DB) UpsertProfile(p *model.Profile) error {
	res, err := d.conn.Exec(`
INSERT INTO profiles (model_id, name, port, host, context_size, ngl,
    batch_size, ubatch_size, cache_type_k, cache_type_v,
    flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
    no_kv_offload, use_mmproj, extra_flags)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(model_id, name) DO UPDATE SET
    port=excluded.port, host=excluded.host, context_size=excluded.context_size,
    ngl=excluded.ngl, batch_size=excluded.batch_size, ubatch_size=excluded.ubatch_size,
    cache_type_k=excluded.cache_type_k, cache_type_v=excluded.cache_type_v,
    flash_attn=excluded.flash_attn, jinja=excluded.jinja,
    temperature=excluded.temperature, reasoning_budget=excluded.reasoning_budget,
    top_p=excluded.top_p, top_k=excluded.top_k,
    no_kv_offload=excluded.no_kv_offload, use_mmproj=excluded.use_mmproj,
    extra_flags=excluded.extra_flags
`, p.ModelID, p.Name, p.Port, p.Host, p.ContextSize, p.NGL,
		p.BatchSize, p.UBatchSize, p.CacheTypeK, p.CacheTypeV,
		boolToInt(p.FlashAttn), boolToInt(p.Jinja),
		p.Temperature, p.ReasoningBudget, p.TopP, p.TopK,
		boolToInt(p.NoKVOffload), boolToInt(p.UseMmproj), p.ExtraFlags)
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
		&p.ID, &p.ModelID, &p.Name, &p.Port, &p.Host, &p.ContextSize, &p.NGL,
		&p.BatchSize, &p.UBatchSize, &p.CacheTypeK, &p.CacheTypeV,
		&flashAttn, &jinja, &p.Temperature, &p.ReasoningBudget, &p.TopP, &p.TopK,
		&noKVOffload, &useMmproj, &p.ExtraFlags,
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
