package session

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/prompts"
	"github.com/google/uuid"
)

func taskChecklistContextForCompaction(state contracts.TaskChecklistState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode task checklist context: %w", err)
	}
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, data); err != nil {
		return "", fmt.Errorf("escape task checklist context: %w", err)
	}
	tpl, err := template.New("task_checklist_context").Parse(`<task_checklist source="harness">
{{.}}
</task_checklist>`)
	if err != nil {
		return "", fmt.Errorf("parse task checklist context template: %w", err)
	}
	var output strings.Builder
	if err := tpl.Execute(&output, escaped.String()); err != nil {
		return "", fmt.Errorf("render task checklist context: %w", err)
	}
	return output.String(), nil
}

func continuationContext(summary, checklist string) (string, error) {
	tpl, err := template.New("continuation_context").Parse(`<context-summary>
{{.Summary}}
</context-summary>

{{.Checklist}}`)
	if err != nil {
		return "", fmt.Errorf("parse continuation context template: %w", err)
	}
	var output strings.Builder
	if err := tpl.Execute(&output, struct {
		Summary   string
		Checklist string
	}{summary, checklist}); err != nil {
		return "", fmt.Errorf("render continuation context: %w", err)
	}
	return output.String(), nil
}

// compactionCommit is the durable checkpoint one compaction produces.
type compactionCommit struct {
	Summary       string
	TaskChecklist contracts.TaskChecklistState
	History       []contracts.ChatMessage
}

func (s *InfaiAgentSession) modelClient() contracts.InfaiModelAdaptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *InfaiAgentSession) shouldCompact(usage *contracts.TokenUsage) bool {
	if usage == nil {
		return false
	}
	window := s.modelClient().GetModelSpecs().Model().MaxContextLength
	if window == 0 {
		return false
	}
	used := usage.TotalTokens
	if used == 0 {
		used = usage.PromptTokens + usage.CompletionTokens
	}
	return float64(used) >= float64(window)*0.8
}

// CompactChat runs a manual compaction. The agent loop is parked while the
// session is idle, so the replacement history is installed and acknowledged
// before the session becomes runnable again.
func (s *InfaiAgentSession) ManualCompactChat(ctx context.Context) error {
	s.mu.Lock()
	if s.status != contracts.SessionIdle {
		s.mu.Unlock()
		return fmt.Errorf("session must be in idle state")
	}
	if !s.agentMailbox.IsEmpty() {
		s.mu.Unlock()
		return errors.New("session has queued messages")
	}
	s.agentMailbox.PreventDraining()
	s.mu.Unlock()

	trigger := "user triggered compaction"
	s.publish(contracts.EventStream{Kind: contracts.EventManualCompactionTriggered, Timestamp: time.Now().UTC(), Content: &trigger})

	replacement, summary, err := s.compactChat(ctx, false)
	if err != nil {
		s.finishManualCompaction("", err)
		return err
	}
	if err := s.agent.ReplaceHistory(s.ctx, replacement); err != nil {
		s.l.ErrorContext(s.ctx, "session: failed to install compaction history", "error", err)
		s.finishManualCompaction("", err)
		return err
	}
	// Published after the install so the session is never reported runnable
	// while a stale history is still installed.
	s.finishManualCompaction(summary, nil)
	return nil
}

func (s *InfaiAgentSession) finishManualCompaction(summary string, compactionErr error) {
	s.agentMailbox.AllowDraining()
	s.publishCompactionResult(false, summary, compactionErr)
}

// publishCompactionResult reports how a compaction ended. Every compaction
// publishes exactly one of these: a summary, an empty result for a history with
// nothing to fold, or the error that stopped it. Without it the session would
// stay in SessionCompacting forever, because the hub is the only writer of the
// status and this event is the only thing that moves it back.
func (s *InfaiAgentSession) publishCompactionResult(automatic bool, summary string, compactionErr error) {
	result := contracts.CompactionResult{Summary: summary, Automatic: automatic}
	if compactionErr != nil {
		result.Err = compactionErr.Error()
	}
	s.publish(contracts.EventStream{
		Kind:       contracts.EventCompactionExecuted,
		Timestamp:  time.Now().UTC(),
		Compaction: &result,
	})
}

// autoCompact is the agent loop's mid-turn compaction callback. The loop owns
// its history, so this hands the continuation back instead of pushing it
// through ReplaceHistory.
func (s *InfaiAgentSession) autoCompact(ctx context.Context) ([]contracts.ChatMessage, error) {
	replacement, summary, err := s.compactChat(ctx, true)
	if err != nil {
		// The history is unchanged and the loop keeps running, so the session
		// is runnable again rather than concluded: a compaction can fail on a
		// provider that is simply down.
		s.publishCompactionResult(true, "", err)
		return nil, err
	}
	s.publishCompactionResult(true, summary, nil)
	return replacement, nil
}

// compactChat performs one compaction attempt. It returns the history the
// agent loop must continue from, and the summary the caller publishes once it
// has actually installed that history. An empty summary means there was
// nothing to fold and the history is returned unchanged.
func (s *InfaiAgentSession) compactChat(requestCtx context.Context, automatic bool) ([]contracts.ChatMessage, string, error) {
	if requestCtx != nil {
		select {
		case <-requestCtx.Done():
			return nil, "", requestCtx.Err()
		default:
		}
	}

	s.mu.Lock()

	if s.pendingBranchParent != uuid.Nil {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("session: submit a message on the selected branch before compacting")
	}
	history := append([]contracts.ChatMessage(nil), s.activeTimeline...)
	checklist := s.taskChecklist.Snapshot()
	s.mu.Unlock()

	ctx := s.ctx
	previous, err := s.timeline.LastCompactionSummary()
	if err != nil {
		return nil, "", err
	}
	toCompact, retained := prompts.PlanCompaction(history, automatic)
	if len(toCompact) == 0 {
		// Nothing to fold, so the loop keeps the history it already has.
		return history, "", nil
	}

	checklistContext, err := taskChecklistContextForCompaction(checklist)
	if err != nil {
		return nil, "", err
	}
	systemPrompt, summaryHistory, err := prompts.CompactionInput(toCompact, previous, checklistContext)
	if err != nil {
		return nil, "", err
	}
	summary, err := s.summarize(ctx, systemPrompt, summaryHistory)
	if err != nil {
		return nil, "", err
	}
	if summary == "" {
		return nil, "", fmt.Errorf("session: compaction produced an empty summary")
	}
	continuation, err := continuationContext(summary, checklistContext)
	if err != nil {
		return nil, "", err
	}
	replacement := []contracts.ChatMessage{contracts.NewUserMessage(continuation)}
	replacement = append(replacement, retained...)

	if err := s.commitCompaction(compactionCommit{Summary: summary, TaskChecklist: checklist, History: replacement}); err != nil {
		return nil, "", err
	}
	return replacement, summary, nil
}

func (s *InfaiAgentSession) summarize(ctx context.Context, systemPrompt string, history []contracts.ChatMessage) (string, error) {
	messages := make([]contracts.ChatMessage, 0, len(history)+1)
	messages = append(messages, contracts.NewSystemMessage(systemPrompt))
	messages = append(messages, history...)

	reply, _, err := s.modelClient().Generate(ctx, messages, nil, &contracts.GenerateOptions{})
	if err != nil {
		return "", err
	}
	return reply.Text(), nil
}
