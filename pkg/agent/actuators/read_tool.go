package actuators

import (
	"context"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func ReadTool() contracts.Tool {
	return contracts.Tool{
		Name:        "read",
		Description: "Read a UTF-8 text file",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to the workspace",
				},
			},
			RequiredFields:       []string{"path"},
			AdditionalProperties: false,
		},
	}
}

func readExecution(ctx context.Context) (string, error) {
	// The workspace-aware file read will be implemented in this workflow.
	return "", nil
}
