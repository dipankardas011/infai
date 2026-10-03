package delegate

import (
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestSidecarAgentNameValidation(t *testing.T) {
	for _, name := range []string{string(contracts.SpawnSidecarLoopTool), string(contracts.SpawnBackgroundSidecarLoopTool)} {
		for _, tc := range []struct {
			name string
			want string
			ok   bool
		}{
			{"spaces", "   ", false},
			{"too long", strings.Repeat("界", 71), false},
			{"unicode boundary", strings.Repeat("界", 70), true},
			{"trimmed", "  worker  ", true},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				call := contracts.ToolCall{Function: contracts.Function{
					Name:      contracts.ToolType(name),
					Arguments: `{"agent_name":"` + tc.want + `","task":"work","acceptance_script":"true","max_turns":20}`,
				}}
				args, err := parseArgs(call)
				if (err == nil) != tc.ok {
					t.Fatalf("parseArgs error = %v, want valid = %t", err, tc.ok)
				}
				if tc.name == "trimmed" && args.AgentName != "worker" {
					t.Fatalf("agent name = %q, want worker", args.AgentName)
				}
			})
		}
	}
}
