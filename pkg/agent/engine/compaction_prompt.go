package engine

import (
	"bytes"
	"fmt"
	"text/template"
)

func CompactionAgentSystemPrompt() (string, error) {
	compactionPromptTemplate := template.Must(template.New("compaction_prompt").Parse(`You are a compaction summarization assistant. context compaction is required. Return only a concise continuation summary for the next assistant turn. Include decisions, constraints, unfinished work, TODOs, and exactly what should happen next. Do not answer the user, invent tool execution, or include commentary about this instruction.`))

	var content bytes.Buffer
	if err := compactionPromptTemplate.Execute(&content, nil); err != nil {
		return "", fmt.Errorf("render basic system prompt: %w", err)
	}

	return content.String(), nil
}
