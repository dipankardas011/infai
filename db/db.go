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
	_, err := d.conn.Exec(`
CREATE TABLE IF NOT EXISTS models (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    dir_name     TEXT NOT NULL,
    gguf_path    TEXT NOT NULL UNIQUE,
    mmproj_path  TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL
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

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO settings VALUES ('server_bin', '/home/dipankardas/llama.cpp/build/bin/llama-server');
INSERT OR IGNORE INTO settings VALUES ('models_dir', '/home/dipankardas/ws/vllm');
INSERT OR IGNORE INTO settings VALUES ('theme', 'tokyonight');
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

func (d *DB) UpsertModel(m *model.ModelEntry) error {
	res, err := d.conn.Exec(`
INSERT INTO models (dir_name, gguf_path, mmproj_path, display_name)
VALUES (?, ?, ?, ?)
ON CONFLICT(gguf_path) DO UPDATE SET
    dir_name=excluded.dir_name,
    mmproj_path=excluded.mmproj_path,
    display_name=excluded.display_name
`, m.DirName, m.GGUFPath, m.MmprojPath, m.DisplayName)
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
	rows, err := d.conn.Query(`SELECT id, dir_name, gguf_path, mmproj_path, display_name FROM models ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelEntry
	for rows.Next() {
		var m model.ModelEntry
		if err := rows.Scan(&m.ID, &m.DirName, &m.GGUFPath, &m.MmprojPath, &m.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, m)
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
