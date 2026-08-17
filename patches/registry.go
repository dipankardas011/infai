package patches

import (
	"database/sql"
	"fmt"

	"github.com/dipankardas011/infai/config"
)

type Patch struct {
	Version int
	Name    string
	Apply   func(*sql.DB) error
}

var All = []Patch{
	{Version: 5, Name: "typed_model_artifacts", Apply: m0005},
	{Version: 6, Name: "copy_to_model_registry", Apply: m0006},
	{Version: 8, Name: "speculative_profile_flags", Apply: m0008},
}

func Apply(db *sql.DB, fromVersion, toVersion int) error {
	min := max(fromVersion, config.MinSchemaVersion)

	for _, p := range All {
		if p.Version > min && p.Version <= toVersion {
			if err := p.Apply(db); err != nil {
				return fmt.Errorf("patch %d (%s): %w", p.Version, p.Name, err)
			}
		}
	}
	return nil
}
