ALTER TABLE profiles ADD COLUMN speculative_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE profiles ADD COLUMN draft_model_id INTEGER REFERENCES model_registry(id) ON DELETE SET NULL;
ALTER TABLE profiles ADD COLUMN speculative_tokens INTEGER;
