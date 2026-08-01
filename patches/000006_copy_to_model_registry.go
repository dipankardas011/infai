package patches

import (
	"database/sql"
	"fmt"
)

func m0006(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'model_registry'`).Scan(&exists); err != nil {
		return fmt.Errorf("m0006 check model_registry: %w", err)
	}
	if exists == 0 {
		return nil
	}

	if _, err := db.Exec(`
		INSERT OR IGNORE INTO model_registry
		    (id, scan_dir, model_dir, primary_file, mmproj_path, display_name, type, metadata,
		     source_repo, source_revision, source_files, last_used, last_verified)
		SELECT id, scan_dir, model_dir, primary_file, mmproj_path, display_name, type, metadata,
		       source_repo, source_revision, source_files, last_used, last_verified
		FROM models
	`); err != nil {
		return fmt.Errorf("m0006 copy: %w", err)
	}

	return nil
}
