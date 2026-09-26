package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// LoadSkillExecution loads the requested skill body from the registry. Names
// are validated against the registry; a skill call can never read an arbitrary
// path.
func (sr *SkillRegistry) LoadSkillExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[readSkillArguments](contracts.ReadSkillTool, tc)
	if err != nil {
		return "", err
	}
	if args.Name == "" {
		return "", errors.New("read_skill requires a non-empty name")
	}
	return contracts.RunBounded(ctx, contracts.ReadSkillTool, time.Second, func() (string, error) {
		return sr.readSkill(args.Name)
	})
}

// readSkill returns the immutable SKILL.md body captured at session startup.
func (r *SkillRegistry) readSkill(name string) (string, error) {
	content, ok := r.contents[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	return content, nil
}
