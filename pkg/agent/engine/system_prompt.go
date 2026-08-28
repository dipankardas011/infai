package engine

import (
	"bytes"
	"fmt"
	"text/template"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func GetBasicSystemPrompt(tools []contracts.Tool, skills []contracts.Skill) (string, error) {
	type systemPromptData struct {
		DateUTC   string
		LocalTime string
		Skills    []contracts.Skill
		Tools     []contracts.Tool
	}

	tpl, err := template.New("basic_sys_prompt").Parse(`You are a helpful assistant running on infai agent engine.
Todays date: {{.DateUTC}}
LocalTime: {{.LocalTime}}

Be Honest if you don't know about it you don't answer just ask for facts

<skills>
{{range .Skills}}- {{.Title}}: {{.Description}}
{{end}}</skills>

<tool_calls>
{{range .Tools}}- {{.Name}}: {{.Description}}
{{end}}</tool_calls>
`)
	if err != nil {
		return "", fmt.Errorf("parse basic system prompt: %w", err)
	}

	data := systemPromptData{
		DateUTC:   time.Now().UTC().Format(time.RFC3339),
		LocalTime: time.Now().Local().Format(time.RFC3339),
		Skills:    skills,
		Tools:     tools,
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render basic system prompt: %w", err)
	}

	return buf.String(), nil
}
