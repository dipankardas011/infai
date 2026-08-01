// Package patches contains data migration code paired with specific schema
// migration versions. Each file corresponds to one migration and can be
// deleted once the migration has been applied across all installations.
package patches

import (
	"database/sql"
	"fmt"
	"path/filepath"
)

// M0005 splits GGUF model paths into model_dir + primary_file.
// Migration 000005 copies gguf_path into model_dir; this patch extracts the
// directory and filename for GGUF records (e.g. /a/b/c.gguf → dir=/a/b, file=c.gguf).
//
// Safetensors / hf_quantized records already had their directory in gguf_path;
// model_dir stays as-is and primary_file remains empty for those.
func m0005(db *sql.DB) error {
	var needs int
	if err := db.QueryRow(`SELECT count(*) FROM models WHERE model_dir != '' AND primary_file = '' AND type IN ('gguf', 'gguf_multimodal') AND model_dir LIKE '%.gguf'`).Scan(&needs); err != nil {
		return fmt.Errorf("m0005 check: %w", err)
	}
	if needs == 0 {
		return nil
	}

	rows, err := db.Query(`SELECT id, model_dir FROM models WHERE type IN ('gguf', 'gguf_multimodal') AND model_dir LIKE '%.gguf'`)
	if err != nil {
		return fmt.Errorf("m0005 query: %w", err)
	}
	defer rows.Close()

	type fixup struct {
		id    int64
		gDir  string
		gFile string
	}
	var fixes []fixup
	for rows.Next() {
		var id int64
		var md string
		if err := rows.Scan(&id, &md); err != nil {
			return fmt.Errorf("m0005 scan: %w", err)
		}
		fixes = append(fixes, fixup{id: id, gDir: filepath.Dir(md), gFile: filepath.Base(md)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("m0005 rows: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("m0005 tx: %w", err)
	}
	defer tx.Rollback()

	for _, f := range fixes {
		if _, err := tx.Exec(`UPDATE models SET model_dir = ?, primary_file = ? WHERE id = ?`, f.gDir, f.gFile, f.id); err != nil {
			return fmt.Errorf("m0005 update id=%d: %w", f.id, err)
		}
	}
	return tx.Commit()
}
