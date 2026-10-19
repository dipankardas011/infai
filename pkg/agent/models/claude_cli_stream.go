package models

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

// claudeToolCallID mints the identifier infai uses to pair a tool result with
// the call that asked for it. The CLI reports no id of its own, because the
// request never left infai's control.
func claudeToolCallID() string {
	var value [6]byte
	for i := range value {
		value[i] = byte(rand.UintN(256))
	}
	return "call_" + hex.EncodeToString(value[:])
}

type claudeStreamResult struct {
	Output     *claudeStructuredOutput
	Reasoning  string
	Usage      *contracts.TokenUsage
	ResultSeen bool
	Retryable  bool
	RateLimit  string
	// SessionID is the CLI conversation this process used. A fresh turn names
	// a new one; a resumed turn reports the one it appended to.
	SessionID string
	// StepUsage is the last assistant message's own usage. The result event
	// is cumulative for the whole process, and iterations is optional, so
	// this is the fallback for measuring the context window.
	StepUsage    claudeTokenUsage
	HasStepUsage bool
	// NativeTool is a call the model made through the CLI's own tool channel
	// instead of the output schema. The channel is disabled, so the CLI answers
	// "No such tool available"; infai still honours the call because it names a
	// tool infai offered, and the model's argument object is already the one the
	// tool expects. NativeText is what the model said in that same turn.
	NativeTool *claudeNativeToolCall
	NativeText string
}

type claudeNativeToolCall struct {
	Name  string
	Input json.RawMessage
}

type claudeStreamEvent struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	Error            json.RawMessage `json:"error"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Event            struct {
		Type  string `json:"type"`
		Delta struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Content []struct {
			Type     string          `json:"type"`
			Thinking string          `json:"thinking"`
			Text     string          `json:"text"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		Usage claudeTokenUsage `json:"usage"`
	} `json:"message"`
	Usage         claudeTokenUsage `json:"usage"`
	RateLimitInfo struct {
		Status   string `json:"status"`
		ResetsAt string `json:"resets_at"`
	} `json:"rate_limit_info"`
}

type claudeTokenUsage struct {
	InputTokens              uint64 `json:"input_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	// Iterations are the requests of the last API call. The top level sums
	// every request in one -p process, so it reports what the turn cost, not
	// how full the context window was.
	Iterations []claudeTokenUsage `json:"iterations"`
}

// decodeClaudeStream consumes the CLI's newline-delimited JSON for one turn.
//
// The visible answer and any tool request are carried by the final result
// event's validated object, so they are not emitted here: streaming only
// forwards reasoning and provider status. The raw structured JSON that streams
// in between must never reach the client, which is why nothing else is
// forwarded.
//
// A tool_use block for a tool in allowed is not forwarded either: it is the
// model skipping the schema for the CLI's native channel, which is disabled.
// The call is captured and stop ends the turn before the CLI can reject it and
// the model starts retrying.
func decodeClaudeStream(ctx context.Context, reader io.Reader, opts *contracts.GenerateOptions, allowed claudeTools, stop func()) (claudeStreamResult, error) {
	scanner := claudeScanner(reader)
	var reasoning strings.Builder
	var result claudeStreamResult

	emit := func(kind contracts.EventStreamKind, text string) bool {
		return text != "" && opts != nil && opts.OnDelta != nil && opts.OnDelta(kind, text)
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var event claudeStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return result, fmt.Errorf("claude cli: could not read an event: %w", err)
		}
		switch event.Type {
		case "stream_event":
			if event.Event.Type != "content_block_delta" || event.Event.Delta.Type != "thinking_delta" || event.Event.Delta.Thinking == "" {
				continue
			}
			reasoning.WriteString(event.Event.Delta.Thinking)
			if emit(contracts.DeltaReasoning, event.Event.Delta.Thinking) {
				return result, harnessErr.ErrTurnCanceled
			}
		case "assistant":
			if !claudeUsageEmpty(event.Message.Usage) {
				result.StepUsage = event.Message.Usage
				result.HasStepUsage = true
			}
			for _, block := range event.Message.Content {
				switch block.Type {
				case "thinking":
					// Only a fallback source of reasoning: the validated answer
					// is read from the result event, which is authoritative.
					if reasoning.Len() == 0 && block.Thinking != "" {
						reasoning.WriteString(block.Thinking)
					}
				case "text":
					result.NativeText += block.Text
				case "tool_use":
					if _, ok := allowed[block.Name]; !ok {
						continue
					}
					result.NativeTool = &claudeNativeToolCall{Name: block.Name, Input: block.Input}
					// No result event follows a stopped turn, so the cost of
					// the call that produced this tool_use is read here.
					result.Usage = claudeUsage(event.Message.Usage, nil)
					result.Reasoning = reasoning.String()
					if stop != nil {
						stop()
					}
					return result, nil
				}
			}
		case "rate_limit_event":
			if event.RateLimitInfo.Status != "" && event.RateLimitInfo.Status != "allowed" {
				result.RateLimit = claudeRateLimitMessage(event.RateLimitInfo.ResetsAt)
				if emit(contracts.EventProviderEvent, result.RateLimit) {
					return result, harnessErr.ErrTurnCanceled
				}
			}
		case "result":
			result.ResultSeen = true
			var step *claudeTokenUsage
			if result.HasStepUsage {
				step = &result.StepUsage
			}
			result.Usage = claudeUsage(event.Usage, step)
			if event.IsError || strings.HasPrefix(event.Subtype, "error") {
				return result, claudeResultError(event, &result)
			}
			output, err := decodeClaudeOutput(event.StructuredOutput)
			if err != nil {
				return result, err
			}
			result.Output = output
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("claude cli: reading its output: %w", err)
	}
	result.Reasoning = reasoning.String()
	return result, nil
}

// claudeUsage reports what the turn cost. Cache reads and writes are input the
// model had to read, so they count towards the prompt side.
func claudeUsage(usage claudeTokenUsage, step *claudeTokenUsage) *contracts.TokenUsage {
	prompt := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	if prompt == 0 && usage.OutputTokens == 0 {
		return nil
	}
	total := &contracts.TokenUsage{
		PromptTokens:     prompt,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      prompt + usage.OutputTokens,
		ContextTokens:    prompt + usage.OutputTokens,
	}
	switch {
	case len(usage.Iterations) > 0:
		last := usage.Iterations[len(usage.Iterations)-1]
		total.ContextTokens = last.InputTokens + last.CacheCreationInputTokens +
			last.CacheReadInputTokens + last.OutputTokens
	case step != nil:
		total.ContextTokens = step.InputTokens + step.CacheCreationInputTokens +
			step.CacheReadInputTokens + step.OutputTokens
	}
	if usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 {
		total.PromptTokensDetails = &contracts.PromptTokensDetails{
			CachedTokens:     usage.CacheReadInputTokens,
			CacheWriteTokens: usage.CacheCreationInputTokens,
		}
	}
	return total
}

func claudeUsageEmpty(usage claudeTokenUsage) bool {
	return usage.InputTokens == 0 && usage.CacheCreationInputTokens == 0 &&
		usage.CacheReadInputTokens == 0 && usage.OutputTokens == 0
}

// claudeResultError turns a failed result event into an actionable error,
// marking overload and 5xx failures as retryable.
func claudeResultError(event claudeStreamEvent, result *claudeStreamResult) error {
	message := event.Result
	if message == "" && len(event.Error) > 0 {
		message = string(event.Error)
	}
	if result.RateLimit != "" {
		return errors.New("claude cli: " + result.RateLimit)
	}
	result.Retryable = isClaudeTransientError(event.Subtype + " " + string(event.Error) + " " + message)
	detail := strings.TrimSpace(event.Subtype + " " + message)
	if detail == "" {
		detail = "the turn failed without a message"
	}
	return fmt.Errorf("claude cli: %s", detail)
}

func decodeClaudeOutput(raw json.RawMessage) (*claudeStructuredOutput, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("claude cli: the result carried no structured output")
	}
	var output claudeStructuredOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, fmt.Errorf("claude cli: could not read the structured output: %w", err)
	}
	return &output, nil
}

func claudeRateLimitMessage(reset string) string {
	if reset == "" {
		return "Claude subscription limit reached"
	}
	if parsed, err := time.Parse(time.RFC3339, reset); err == nil {
		return "Claude subscription limit reached, resets at " + parsed.Local().Format("15:04")
	}
	return "Claude subscription limit reached, resets at " + reset
}
