package engine

import (
	"fmt"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// Region-based compaction: the most recent conversation turns — the active
// exchange — stay raw in context, and only the older prefix is folded into a
// single evolving checkpoint. Repeating compactions re-summarize the *prefix*,
// never the sharp tail, so there is no summary-of-summary loss.
const (
	// retentionEvents is how many recent user/assistant events stay raw. Their
	// interleaved tool results are kept too; everything older is summarized.
	retentionEvents = 4

	contextSummaryOpenTag = "<context-summary>"
)

// planCompaction splits history for compaction. keepRecentTurns keeps the last
// retentionEvents turns raw; manual /compact folds everything after the
// checkpoint message. Never returns nil slices.
func planCompaction(history []contracts.ChatMessage, keepRecentTurns bool) (toCompact, retained []contracts.ChatMessage) {
	start := 0
	if len(history) > 0 && strings.HasPrefix(history[0].Text(), contextSummaryOpenTag) {
		start = 1
	}
	if start >= len(history) {
		return []contracts.ChatMessage{}, []contracts.ChatMessage{}
	}
	if !keepRecentTurns {
		return history[start:], []contracts.ChatMessage{}
	}
	split := start
	seen := 0
	for i := len(history) - 1; i >= start; i-- {
		if history[i].Role == "user" || history[i].Role == "assistant" {
			seen++
			if seen == retentionEvents {
				split = i
				break
			}
		}
	}

	return history[start:split], history[split:]
}

func CompactionAgentSystemPrompt() (string, error) {
	const prompt = `You are a compaction summarizer. Your only job is to turn the conversation given to you into a continuation checkpoint that the very next turn of the same agent can continue from without re-reading the original conversation.

You have exactly one turn. Output ONLY the checkpoint — no tool calls, no questions, no commentary about this instruction.

Rules:
- Follow the structure in the user's instruction exactly, sections in order. Use terse bullets, not prose.
- Write "(none)" for any empty section — never drop a section.
- Preserve exact file paths, symbols, commands, error strings, URLs, and identifiers verbatim.
- Quote the user's requests and corrections verbatim wherever exact wording matters.
- Never mention this summarization request, or that the conversation was compacted.
- If the conversation already contains a prior checkpoint, merge it: keep still-true facts, drop stale ones, and prefer the newer conversation on conflict.`
	return prompt, nil
}

// BuildCompactionInstruction returns the user-prompt half of compaction: the
// 7 Ws checkpoint structure the summarizer must output, in order. It is
// appended after the conversation history. The task checklist, when provided,
// is placed last because it is authoritative over narrative summaries.
func BuildCompactionInstruction(previousSummary, taskChecklist string) (string, error) {
	tpl, err := template.New("compaction_instruction").Parse(`Condense the conversation above into a continuation checkpoint covering the 7 Ws. Output exactly this structure, in order, with terse bullets ("(none)" when empty):

## Why - Goal
- [the user's objective; quote verbatim where exact wording matters]

## What - State
- [done, in progress, key decisions and their rationale]

## Who - Owner
- [which agent owns the open work (main agent plans and reasons, subagents execute); "(none)" if no open work]

## Whom - Continuation
- [what the next turn must know to continue without re-reading this conversation; "(none)" if not needed]

## When - Timing
- [what was in progress at this checkpoint and the order to resume it; deadlines if any]

## Where - Files
- [exact paths touched or relevant; preserve symbols, commands, and error strings verbatim]

## How - Next
1. [the immediate next action, directly in line with the most recent request; "(none)" if concluded]
2. [known follow-ups; "(none)"]
{{with .PreviousSummary}}

A prior checkpoint already exists:

<prior-summary>
{{.}}
</prior-summary>

Merge it into the new checkpoint: preserve everything still true, drop what is finished or stale, and where the conversation conflicts with the prior checkpoint, the conversation wins.
{{end}}
{{with .TaskChecklist}}

The harness-provided task checklist below is authoritative. Use it when describing current state and next actions. Do not invent, remove, rename, or change the status of its items. An empty items array means no checklist work remains.

{{.}}
{{end}}`)
	if err != nil {
		return "", fmt.Errorf("parse compaction instruction template: %w", err)
	}
	var instruction strings.Builder
	if err := tpl.Execute(&instruction, struct {
		PreviousSummary string
		TaskChecklist   string
	}{previousSummary, taskChecklist}); err != nil {
		return "", fmt.Errorf("render compaction instruction: %w", err)
	}
	return instruction.String(), nil
}

// SerializeForCompaction condenses the prefix for the summarizer. The prefix is
// never sent raw: compaction fires at the context limit, so the raw prefix may
// not fit the summarizer's own window.
//
// Policy:
//   - system messages are dropped (the summarizer has its own system prompt)
//   - user messages are kept verbatim (capped)
//   - assistant text is kept, capped; tool-call arguments are reduced to names
//   - reasoning content is dropped (low value for a continuation checkpoint)
//   - tool results: kept fully when short, otherwise the HEAD with a
//     trailing ...(truncated) marker, matching user/assistant truncation
func SerializeForCompaction(history []contracts.ChatMessage) string {
	const (
		maxUserChars      = 4000
		maxAssistantChars = 2000
		dropToolOverChars = 1200
		keepToolHeadChars = 600
	)
	var b strings.Builder
	for _, m := range history {
		switch m.Role {
		case "system":
			continue
		case "user":
			b.WriteString("User: ")
			b.WriteString(truncateRunes(m.Text(), maxUserChars))
		case "assistant":
			b.WriteString("Assistant")
			if len(m.ToolCalls) > 0 {
				names := make([]string, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					names = append(names, tc.Function.Name)
				}
				b.WriteString(" called ")
				b.WriteString(strings.Join(names, ", "))
			}
			b.WriteString(": ")
			b.WriteString(truncateRunes(m.Text(), maxAssistantChars))
		case "tool":
			b.WriteString("Tool result")
			if m.ToolCallID != "" {
				b.WriteString(" [")
				b.WriteString(m.ToolCallID)
				b.WriteString("]")
			}
			b.WriteString(": ")
			text := m.Text()
			if utf8.RuneCountInString(text) <= dropToolOverChars {
				b.WriteString(text)
			} else {
				b.WriteString(truncateRunes(text, keepToolHeadChars))
			}
		default:
			continue
		}
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// compactionInput assembles the summarizer's request: the serialized toCompact
// messages wrapped in <conversation> tags, followed by the 7 Ws instruction.
func compactionInput(toCompact []contracts.ChatMessage, prevCheckpoint, taskChecklist string) (string, []contracts.ChatMessage, error) {
	systemPrompt, err := CompactionAgentSystemPrompt()
	if err != nil {
		return "", nil, err
	}
	transcript := SerializeForCompaction(toCompact)
	instruction, err := BuildCompactionInstruction(prevCheckpoint, taskChecklist)
	if err != nil {
		return "", nil, err
	}
	tpl, err := template.New("compaction_input").Parse(`<conversation>
{{.Transcript}}
</conversation>

{{.Instruction}}`)
	if err != nil {
		return "", nil, fmt.Errorf("parse compaction input template: %w", err)
	}
	var prompt strings.Builder
	if err := tpl.Execute(&prompt, struct {
		Transcript  string
		Instruction string
	}{transcript, instruction}); err != nil {
		return "", nil, fmt.Errorf("render compaction input: %w", err)
	}
	return systemPrompt, []contracts.ChatMessage{contracts.NewUserMessage(prompt.String())}, nil
}

// truncateRunes caps a string at n runes, marking the cut.
func truncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return s[:n] + " ...(truncated)"
}
