package engine

import (
	"os"
	"testing"
)

// These tests create real sessions, and a session loads the tools config from
// the user's config directory — which would open the developer's own MCP
// servers on every run. Point that somewhere empty instead.
// § os.UserConfigDir honors XDG_CONFIG_HOME on Linux.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "infai-engine-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
