package session

import (
	"os"
	"testing"
)

// Sessions load the tools config from the user's config directory. Point that
// somewhere empty so tests never connect to the developer's real MCP servers.
// § os.UserConfigDir honors XDG_CONFIG_HOME on Linux.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "infai-session-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
