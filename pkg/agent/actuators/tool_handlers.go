package actuators

import "github.com/dipankardas011/infai/pkg/agent/contracts"

// ToolFunction is the temporary actuator implementation shape. It will gain
// context and arguments when real actuator execution is added.
type ToolFunction func() string

func ResolveTool(toolType contracts.ToolType) (ToolFunction, bool) {
	var toolFunctions = map[contracts.ToolType]ToolFunction{
		contracts.ReadTool: readExecution,
	}

	fn, ok := toolFunctions[toolType]
	return fn, ok
}
