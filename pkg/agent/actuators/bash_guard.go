package actuators

import (
	"regexp"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// maxCommandBytes caps how large a single command may be.
const maxCommandBytes = 16 * 1024

type dangerousPattern struct {
	re     *regexp.Regexp
	reason string
}

// dangerousPatterns is a static blocklist of operations that no legitimate
// workspace task needs. They are rejected before a human is even asked. It is a
// safety net only — the enforcement chokepoint for bash is human approval.
var dangerousPatterns = []dangerousPattern{
	// Fork bombs.
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{`), "a fork bomb"},
	{regexp.MustCompile(`:\s*\|\s*&`), "a fork bomb"},

	// Obfuscated / self-executing payloads (injection vectors).
	{regexp.MustCompile(`(?i)\bbase64\s+(?:-d|--decode)\b[^\n]*\|\s*(?:ba)?sh\b`), "executing a decoded script"},
	{regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n]*\|\s*(?:ba)?sh\b`), "downloading and executing a script"},

	// Host-level destructive operations.
	{regexp.MustCompile(`(?i)\bmkfs(?:\.[a-z0-9_]+)?\b`), "formatting a disk"},
	{regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/`), "writing to a raw block device"},
	{regexp.MustCompile(`(?i)[>|]\s*/dev/(?:sd[a-z]|hd[a-z]|vd[a-z]|mapper/)`), "writing to a raw block device"},
	{regexp.MustCompile(`(?i)\b(?:shutdown|poweroff|reboot|halt)\b`), "shutting down or rebooting the host"},
	{regexp.MustCompile(`(?i)\bkill\s+(?:-[a-z0-9]+\s+)?1\b`), "killing init (PID 1)"},

	// Destructive operations on the filesystem root.
	{regexp.MustCompile(`(?i)(?:^|[;&|]\s*)\b(?:sudo\s+)?rm\b[^\n]*?\s(?:-[^\s]+\s+)*/(?:\s|$)`), "deleting the filesystem root"},
	{regexp.MustCompile(`(?i)\b(?:chmod|chown)\b[^\n]*?\s/(?:\s|$)`), "changing permissions or ownership of the filesystem root"},
}

func checkDangerousCommand(command string) error {
	lower := strings.ToLower(command)
	for _, pattern := range dangerousPatterns {
		if pattern.re.MatchString(lower) {
			return execErr(
				contracts.BashTool,
				"dangerous_command",
				"the command was rejected because it attempts "+pattern.reason,
				ResponsibilityAgent,
				nil,
			)
		}
	}
	return nil
}
