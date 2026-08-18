ALTER TABLE models ADD COLUMN model_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN primary_file TEXT NOT NULL DEFAULT '';

UPDATE models SET model_dir = gguf_path;

ALTER TABLE models ADD COLUMN source_repo TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN source_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN source_files TEXT NOT NULL DEFAULT '[]';
