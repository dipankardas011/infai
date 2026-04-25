-- 1_initial.up.sql
CREATE TABLE IF NOT EXISTS models (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_dir      TEXT NOT NULL DEFAULT '',
    dir_name      TEXT NOT NULL,
    gguf_path     TEXT NOT NULL UNIQUE,
    mmproj_path   TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    last_used     DATETIME DEFAULT CURRENT_TIMESTAMP,
    checksum     TEXT NOT NULL DEFAULT '',
    last_verified DATETIME DEFAULT CURRENT_TIMESTAMP,
    architecture TEXT NOT NULL DEFAULT '',
    model_name    TEXT NOT NULL DEFAULT ''
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
INSERT OR IGNORE INTO settings VALUES ('schema_version', '1');

INSERT OR IGNORE INTO scan_dirs SELECT value FROM settings WHERE key='models_dir' AND value != '';
DELETE FROM settings WHERE key='models_dir';
