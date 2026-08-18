CREATE TABLE IF NOT EXISTS inference_engine (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    path TEXT NOT NULL
);

-- Static UUIDv7 generated with github.com/google/uuid.NewV7().
-- Path is copied from the legacy executors table.
INSERT OR IGNORE INTO inference_engine (id, name, path)
SELECT '019ecc2c-a9ec-794f-973a-ecb69c8ded9d', 'migrated-llamacpp', path
FROM executors
ORDER BY is_default DESC, id
LIMIT 1;

ALTER TABLE profiles ADD COLUMN inference_engine_id TEXT;

UPDATE profiles
SET inference_engine_id = '019ecc2c-a9ec-794f-973a-ecb69c8ded9d'
WHERE inference_engine_id IS NULL;
