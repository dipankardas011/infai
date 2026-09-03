// Package memory houses the agent's knowledge layers.
//
// Skills are the deterministic, curated layer: authored SKILL.md files scanned
// from <home>/.agents/skills and <project>/.agents/skills at session start and
// loaded on demand through the read_skill tool (see read_skill.go). The engine
// never touches skill files directly — it routes read_skill calls here through
// ExecuteMemoryToolCall.
//
// TODO: episodic memory — auto-accumulated facts ("we hit bug X, fixed with Y")
// retrieved by similarity, complementing the deterministic skill layer.
// TODO: long-term memory persistence and recall across sessions.
// TODO: skill hot-reload (watch mtime/hash) instead of scan-on-session-start.
package memory
