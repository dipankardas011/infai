package browserext

import "embed"

// Assets holds the unpacked Chrome MV3 extension; the daemon writes it into
// the extension folder on every start.
//
//go:embed all:assets
var Assets embed.FS
