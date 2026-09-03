package memory

import (
	"context"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type contextKey uint8

const (
	toolCallKey contextKey = iota
	skillRegistryKey
)

// WithToolCall carries the originating tool call so executors can read its args.
func WithToolCall(ctx context.Context, call contracts.ToolCall) context.Context {
	return context.WithValue(ctx, toolCallKey, call)
}

// ToolCallFromContext returns the tool call carried by ctx, if any.
func ToolCallFromContext(ctx context.Context) (contracts.ToolCall, bool) {
	call, ok := ctx.Value(toolCallKey).(contracts.ToolCall)
	return call, ok
}

// WithSkillRegistry carries the session's skill registry for skill executors.
func WithSkillRegistry(ctx context.Context, registry *SkillRegistry) context.Context {
	return context.WithValue(ctx, skillRegistryKey, registry)
}

// SkillRegistryFromContext returns the skill registry carried by ctx, if any.
func SkillRegistryFromContext(ctx context.Context) *SkillRegistry {
	registry, _ := ctx.Value(skillRegistryKey).(*SkillRegistry)
	return registry
}

// ExecuteMemoryToolCall dispatches memory-owned tools (skills today; episodic
// memory later) — the memory-package analog of actuators.ExecuteToolCall.
func ExecuteMemoryToolCall(ctx context.Context, call contracts.ToolCall) (string, error) {
	toolContext := WithToolCall(ctx, call)
	switch contracts.ToolType(call.Function.Name) {
	case contracts.ReadSkillTool:
		return readSkillExecution(toolContext)
	default:
		// TODO: unify error shape with actuators (single ExecutionError type in a
		// shared package) instead of a plain fmt.Errorf.
		return "", fmt.Errorf("unknown memory tool %q", call.Function.Name)
	}
}

func IsMemoryToolCall(c contracts.ToolType) bool {
	switch c {
	case contracts.ReadSkillTool:
		return true
	default:
		return false
	}
}
