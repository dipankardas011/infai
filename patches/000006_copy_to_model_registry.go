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

	if _, err := db.Exec(`
		UPDATE model_registry SET
		    model_dir  = (SELECT model_dir  FROM models WHERE models.id = model_registry.id),
		    primary_file = (SELECT primary_file FROM models WHERE models.id = model_registry.id),
		    display_name = (SELECT display_name FROM models WHERE models.id = model_registry.id),
		    scan_dir     = (SELECT scan_dir     FROM models WHERE models.id = model_registry.id),
		    mmproj_path  = (SELECT mmproj_path  FROM models WHERE models.id = model_registry.id),
		    type         = (SELECT type         FROM models WHERE models.id = model_registry.id),
		    metadata     = (SELECT metadata     FROM models WHERE models.id = model_registry.id),
		    source_repo  = (SELECT source_repo  FROM models WHERE models.id = model_registry.id),
		    source_revision = (SELECT source_revision FROM models WHERE models.id = model_registry.id),
		    source_files    = (SELECT source_files    FROM models WHERE models.id = model_registry.id),
		    last_used       = (SELECT last_used       FROM models WHERE models.id = model_registry.id),
		    last_verified   = (SELECT last_verified   FROM models WHERE models.id = model_registry.id)
		WHERE id IN (SELECT id FROM models)
	`); err != nil {
		return fmt.Errorf("m0006 sync paths: %w", err)
	}

	return nil
}
