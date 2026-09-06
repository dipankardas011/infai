package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// ReadSkillTool describes the read_skill function exposed to the model. The
// location of a skill is internal and never part of the schema; the model asks
// by name only and the registry resolves it.
func ReadSkillTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.ReadSkillTool),
		Description: "Load a skill's full instructions from memory when the current task matches its description. Call with a name from <available_skills>.",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The skill name to load",
				},
			},
			RequiredFields:       []string{"name"},
			AdditionalProperties: false,
		},
	}
}

type readSkillArguments struct {
	Name string `json:"name"`
}

// readSkillExecution loads the requested skill body from the registry carried
// in ctx. Names are validated against the registry; a skill call can never read
// an arbitrary path.
func readSkillExecution(ctx context.Context) (string, error) {
	registry := SkillRegistryFromContext(ctx)
	if registry == nil {
		return "", errors.New("no skill registry in context")
	}
	call, ok := ToolCallFromContext(ctx)
	if !ok {
		return "", errors.New("read_skill: no tool call in context")
	}
	var args readSkillArguments
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("read_skill arguments: %w", err)
	}
	if args.Name == "" {
		return "", errors.New("read_skill requires a non-empty name")
	}
	return registry.readSkill(args.Name)
}

// ReadSkillNameFromCall returns the resolved skill name for UI deltas, falling
// back to the bare tool name (never the raw arguments JSON).
func ReadSkillNameFromCall(call contracts.ToolCall) string {
	var args readSkillArguments
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err == nil && args.Name != "" {
		return args.Name
	}
	return string(contracts.ReadSkillTool)
}

// readSkill returns the immutable SKILL.md body captured at session startup.
func (r *SkillRegistry) readSkill(name string) (string, error) {
	content, ok := r.contents[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	return content, nil
}
