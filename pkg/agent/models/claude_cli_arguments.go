package models

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// claudeArguments repairs the type mistakes Claude makes in tool arguments.
//
// Claude Code's structured output is guided by the schema but not enforced by
// it, so a field declared as an integer routinely arrives as the string "60".
// The tool's own Parameters field is the validator: this walker applies the
// declared JSON type to the value, and a new tool is covered without a switch
// here. MCP tools use the same path; a schema this walker does not describe is
// left untouched.
//
// Only lossless conversions are made. Anything that cannot be translated stays
// as the model wrote it, and infai's strict decoder names the field and both
// types in its error so the model can correct the next turn.
func claudeArguments(tool contracts.Tool, arguments map[string]any) map[string]any {
	if len(arguments) == 0 || len(tool.Parameters) == 0 {
		return arguments
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		return arguments
	}
	return claudeNormalizeObject(schema, arguments)
}

func claudeNormalizeObject(schema map[string]any, value map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	additional, _ := schema["additionalProperties"].(map[string]any)
	for key, item := range value {
		property, ok := properties[key].(map[string]any)
		if !ok {
			property = additional
		}
		if property == nil {
			continue
		}
		value[key] = claudeNormalizeValue(property, item)
	}
	return value
}

func claudeNormalizeValue(schema map[string]any, value any) any {
	switch typed := value.(type) {
	case string:
		return claudeNormalizeString(schema, typed)
	case map[string]any:
		return claudeNormalizeObject(schema, typed)
	case []any:
		switch items := schema["items"].(type) {
		case map[string]any:
			for index := range typed {
				typed[index] = claudeNormalizeValue(items, typed[index])
			}
		case []any:
			for index := 0; index < len(typed) && index < len(items); index++ {
				if itemSchema, ok := items[index].(map[string]any); ok {
					typed[index] = claudeNormalizeValue(itemSchema, typed[index])
				}
			}
		}
		return typed
	default:
		return value
	}
}

func claudeNormalizeString(schema map[string]any, value string) any {
	for _, kind := range claudeSchemaTypes(schema) {
		switch kind {
		case "integer":
			if parsed, ok := claudeInteger(value); ok {
				return parsed
			}
		case "number":
			if parsed, ok := claudeNumber(value); ok {
				return parsed
			}
		case "boolean":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true":
				return true
			case "false":
				return false
			}
		case "object":
			var parsed map[string]any
			if json.Unmarshal([]byte(value), &parsed) == nil && parsed != nil {
				return claudeNormalizeObject(schema, parsed)
			}
		case "array":
			var parsed []any
			if json.Unmarshal([]byte(value), &parsed) == nil && parsed != nil {
				return claudeNormalizeValue(schema, parsed)
			}
		}
	}
	return value
}

// claudeSchemaTypes reads the declared types, including the branches of a
// union. A value is translated by the first branch that describes it.
func claudeSchemaTypes(schema map[string]any) []string {
	var types []string
	add := func(value any) {
		switch typed := value.(type) {
		case string:
			types = append(types, typed)
		case []any:
			for _, item := range typed {
				if name, ok := item.(string); ok {
					types = append(types, name)
				}
			}
		}
	}
	add(schema["type"])
	if len(types) > 0 {
		return types
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		branches, _ := schema[key].([]any)
		for _, branch := range branches {
			if object, ok := branch.(map[string]any); ok {
				add(object["type"])
			}
		}
	}
	return types
}

func claudeInteger(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
		return parsed, true
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) != parsed ||
		parsed < math.MinInt64 || parsed > math.MaxInt64 {
		return 0, false
	}
	return int64(parsed), true
}

func claudeNumber(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}
