package actuators

import (
	"errors"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

type ToolFunction func(arguments string) (string, error)

func ResolveTool(toolType contracts.ToolType) (ToolFunction, bool) {
	var toolFunctions = map[contracts.ToolType]ToolFunction{
		contracts.ReadTool: readExecution,
	}

	fn, ok := toolFunctions[toolType]
	return fn, ok
}

func ExecuteToolCall(call contracts.ToolCall) (string, error) {
	fn, ok := ResolveTool(contracts.ToolType(call.Function.Name))
	if !ok {
		return "", errors.New("tool not found")
	}
	return fn(call.Function.Arguments)
}
