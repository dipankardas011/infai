CREATE TEMP TABLE tmp_profiles AS SELECT * FROM profiles;
DROP TABLE profiles;

CREATE TEMP TABLE tmp_recents AS SELECT * FROM recents;
DROP TABLE recents;

CREATE TABLE profiles (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id           INTEGER NOT NULL REFERENCES model_registry(id) ON DELETE CASCADE,
    inference_engine_id TEXT,
    name              TEXT NOT NULL,
    port              INTEGER NOT NULL DEFAULT 8000,
    host              TEXT NOT NULL DEFAULT '0.0.0.0',
    context_size      INTEGER NOT NULL DEFAULT 65536,
    ngl               TEXT NOT NULL DEFAULT 'auto',
    batch_size        INTEGER,
    ubatch_size       INTEGER,
    cache_type_k      TEXT,
    cache_type_v      TEXT,
    flash_attn        INTEGER NOT NULL DEFAULT 0,
    jinja             INTEGER NOT NULL DEFAULT 0,
    temperature       REAL,
    reasoning_budget  INTEGER,
    top_p             REAL,
    top_k             INTEGER,
    no_kv_offload     INTEGER NOT NULL DEFAULT 0,
    use_mmproj        INTEGER NOT NULL DEFAULT 0,
    extra_flags       TEXT NOT NULL DEFAULT '',
    engine_config     TEXT NOT NULL DEFAULT '',
    UNIQUE(model_id, name)
);

CREATE TABLE recents (
    model_id   INTEGER NOT NULL REFERENCES model_registry(id) ON DELETE CASCADE,
    profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    last_used  DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (model_id, profile_id)
);

INSERT INTO profiles
    (id, model_id, inference_engine_id, name, port, host, context_size, ngl,
     batch_size, ubatch_size, cache_type_k, cache_type_v,
     flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
     no_kv_offload, use_mmproj, extra_flags, engine_config)
SELECT id, model_id, inference_engine_id, name, port, host, context_size, ngl,
       batch_size, ubatch_size, cache_type_k, cache_type_v,
       flash_attn, jinja, temperature, reasoning_budget, top_p, top_k,
       no_kv_offload, use_mmproj, extra_flags, engine_config
FROM tmp_profiles;

INSERT INTO recents SELECT * FROM tmp_recents;

DROP TABLE IF EXISTS models;
