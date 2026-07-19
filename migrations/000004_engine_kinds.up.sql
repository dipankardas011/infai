ALTER TABLE inference_engine ADD COLUMN kind TEXT NOT NULL DEFAULT 'llamacpp';
ALTER TABLE inference_engine ADD COLUMN base_args TEXT NOT NULL DEFAULT '[]';
ALTER TABLE inference_engine ADD COLUMN environment TEXT NOT NULL DEFAULT '{}';

ALTER TABLE profiles ADD COLUMN engine_config TEXT NOT NULL DEFAULT '{}';

-- Older non-GGUF scans used an empty path. Their scan directory is the actual
-- local model directory consumed by engines such as vLLM.
UPDATE models SET gguf_path = scan_dir WHERE gguf_path = '' AND scan_dir != '';
