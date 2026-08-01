CREATE TABLE IF NOT EXISTS model_registry (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_dir       TEXT NOT NULL DEFAULT '',
    model_dir      TEXT NOT NULL DEFAULT '',
    primary_file   TEXT NOT NULL DEFAULT '',
    mmproj_path    TEXT NOT NULL DEFAULT '',
    display_name   TEXT NOT NULL DEFAULT '',
    type           TEXT NOT NULL DEFAULT 'gguf',
    metadata       TEXT NOT NULL DEFAULT '',
    source_repo    TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL DEFAULT '',
    source_files   TEXT NOT NULL DEFAULT '',
    last_used      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_verified  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_path ON model_registry(model_dir, primary_file);
