package config

var version = "dev"

// MinSchemaVersion is the minimum migration version assumed to be present.
// Patches numbered below this are safe to skip for fresh installs (the SQL
// migrations already produce the correct schema). Bump this after removing
// the corresponding patch files.
const MinSchemaVersion = 0

func Version() string {
	return version
}
