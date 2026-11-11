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
	// StreamedContent is the answer text delivered while the turn ran, and
	// ContentStreamed says whether any was. Generate publishes only the part the
	// stream did not already deliver.
	StreamedContent string
	ContentStreamed bool
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
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			Text        string `json:"text"`
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
	Usage         claudeTokenUsage    `json:"usage"`
	RateLimitInfo claudeRateLimitInfo `json:"rate_limit_info"`
}

// claudeRateLimitInfo is the quota snapshot the CLI emits on rate_limit_event.
// The field names are camelCase here even though the event's own top-level
// keys are snake_case: the CLI serializes this nested object from its own
// camelCase schema. resetsAt is epoch seconds.
type claudeRateLimitInfo struct {
	Status         string  `json:"status"`
	ResetsAt       int64   `json:"resetsAt"`
	RateLimitType  string  `json:"rateLimitType"`
	Utilization    float64 `json:"utilization"`
	OverageStatus  string  `json:"overageStatus"`
	IsUsingOverage bool    `json:"isUsingOverage"`
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
// The answer is generated as a JSON object validated against the schema, so
// the partial deltas are JSON fragments. claudeContentStream reads the
// top-level "content" string out of them and it is forwarded as it grows; the
// rest of the JSON (the tool_calls and the syntax around the answer) is never
// forwarded. The final result event remains authoritative for the committed
// reply.
//
// A tool_use block for a tool in allowed is not forwarded: it is the model
// skipping the schema for the CLI's native channel, which is disabled. The call
// is captured and stop ends the turn before the CLI can reject it and the model
// starts retrying.
func decodeClaudeStream(ctx context.Context, reader io.Reader, opts *contracts.GenerateOptions, allowed claudeTools, stop func()) (result claudeStreamResult, err error) {
	scanner := claudeScanner(reader)
	var reasoning strings.Builder

	emit := func(kind contracts.EventStreamKind, text string) bool {
		return text != "" && opts != nil && opts.OnDelta != nil && opts.OnDelta(kind, text)
	}
	// Only the structured-output block is the answer; a native tool's input is
	// JSON too, and must not be read as one.
	content := newClaudeContentStream(func(text string) bool {
		return emit(contracts.DeltaContent, text)
	})
	defer func() {
		result.StreamedContent = content.Content()
		result.ContentStreamed = content.Streamed()
	}()
	streamingContent := false

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
			switch event.Event.Type {
			case "content_block_start":
				streamingContent = event.Event.ContentBlock.Type == "tool_use" &&
					event.Event.ContentBlock.Name == "StructuredOutput"
			case "content_block_stop":
				streamingContent = false
			case "content_block_delta":
				switch event.Event.Delta.Type {
				case "thinking_delta":
					if event.Event.Delta.Thinking == "" {
						continue
					}
					reasoning.WriteString(event.Event.Delta.Thinking)
					if emit(contracts.DeltaReasoning, event.Event.Delta.Thinking) {
						return result, harnessErr.ErrTurnCanceled
					}
				case "input_json_delta":
					if streamingContent && content.Feed(event.Event.Delta.PartialJSON) {
						return result, harnessErr.ErrTurnCanceled
					}
				}
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
			info := event.RateLimitInfo
			switch info.Status {
			case "", "allowed":
				// Healthy snapshot: the CLI emits a steady trickle of these.
			case "allowed_warning":
				// A warning, not a wall: the turn continues. Never treat it as
				// an exhausted limit, or a session would fail at 70-85% used.
				if notice := claudeRateLimitNotice(info); notice != "" {
					if emit(contracts.EventProviderEvent, notice) {
						return result, harnessErr.ErrTurnCanceled
					}
				}
			case "rejected":
				// The only documented status that blocks. Extra usage can still
				// cover it, in which case the turn succeeds on a notice alone.
				if emit(contracts.EventProviderEvent, claudeRateLimitMessage(info)) {
					return result, harnessErr.ErrTurnCanceled
				}
				if !info.IsUsingOverage {
					result.RateLimit = claudeRateLimitMessage(info)
				}
			default:
				// A status this build does not know (a newer CLI). Report it
				// without failing the turn: an unknown word is not evidence
				// that the account is out of quota.
				if notice := claudeRateLimitStatusNotice(info); notice != "" {
					if emit(contracts.EventProviderEvent, notice) {
						return result, harnessErr.ErrTurnCanceled
					}
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
		// The CLI sometimes puts a precise reset or plan note in the result
		// text; keep it rather than discarding what we were handed.
		if detail := strings.TrimSpace(event.Result); detail != "" {
			return fmt.Errorf("claude cli: %s: %s", result.RateLimit, detail)
		}
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

// claudeRateLimitMessage names the window the CLI reports as rejected, the
// share used, and when it says that window lifts. A rejection covered by extra
// usage says so, because the turn still succeeds in that case.
func claudeRateLimitMessage(info claudeRateLimitInfo) string {
	message := "Claude subscription limit reached"
	if window := claudeRateLimitWindow(info.RateLimitType); window != "" {
		message += " (" + window + " limit"
		if info.Utilization > 0 {
			message += ", " + fmt.Sprintf("%.0f%% used", info.Utilization*100)
		}
		message += ")"
	}
	message += claudeRateLimitReset(info.ResetsAt, ", resets ")
	if info.IsUsingOverage {
		message += " (using extra usage)"
	}
	return message
}

// claudeRateLimitNotice is the warning form for a window that is filling up but
// has not blocked the turn. It is never fatal.
func claudeRateLimitNotice(info claudeRateLimitInfo) string {
	notice := "Claude usage is high"
	if window := claudeRateLimitWindow(info.RateLimitType); window != "" {
		notice = "Claude " + window + " usage is high"
	}
	if info.Utilization > 0 {
		notice += fmt.Sprintf(" (%.0f%% used", info.Utilization*100)
		return notice + claudeRateLimitReset(info.ResetsAt, ", resets ") + ")"
	}
	return notice + claudeRateLimitReset(info.ResetsAt, ", resets ")
}

// claudeRateLimitStatusNotice reports a status this build does not recognise,
// so a newer CLI's vocabulary is visible without being treated as a quota wall.
func claudeRateLimitStatusNotice(info claudeRateLimitInfo) string {
	status := strings.TrimSpace(info.Status)
	if status == "" {
		return ""
	}
	notice := "Claude rate limit status: " + status
	if window := claudeRateLimitWindow(info.RateLimitType); window != "" {
		notice += " (" + window + ")"
	}
	return notice + claudeRateLimitReset(info.ResetsAt, ", resets ")
}

// claudeRateLimitWindow humanizes the window the CLI named. An empty result
// means the CLI did not say, so the message omits the window.
func claudeRateLimitWindow(kind string) string {
	switch kind {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "weekly"
	case "seven_day_opus":
		return "weekly (Opus)"
	case "seven_day_sonnet":
		return "weekly (Sonnet)"
	case "seven_day_overage_included":
		return "weekly (with extra usage)"
	case "overage":
		return "extra usage"
	default:
		return strings.TrimSpace(kind)
	}
}

// claudeRateLimitReset renders an epoch reset time. The CLI reports seconds,
// but a millisecond value is still recognised so a unit change cannot produce
// a date thousands of years away.
func claudeRateLimitReset(epoch int64, prefix string) string {
	if epoch <= 0 {
		return ""
	}
	at := time.Unix(epoch, 0)
	if epoch > 1_000_000_000_000 {
		at = time.UnixMilli(epoch)
	}
	local := at.Local()
	if time.Until(at) > 24*time.Hour {
		return prefix + local.Format("Mon 15:04")
	}
	return prefix + local.Format("15:04")
}
