package actuators

import (
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

func readExecution() string {
	return ""
}
