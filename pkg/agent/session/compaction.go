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

func (s *InfaiAgentSession) shouldCompact(usage *contracts.TokenUsage) bool {
	if usage == nil || s.model.GetModelSpecs().Model().MaxContextLength == 0 {
		return false
	}
	used := usage.TotalTokens
	if used == 0 {
		used = usage.PromptTokens + usage.CompletionTokens
	}
	threshold := float64(s.model.GetModelSpecs().Model().MaxContextLength) * 0.8
	return float64(used) >= threshold
}

// CompactChat runs a manual compaction. The agent loop is parked while the
// session is idle, so the replacement history is installed and acknowledged
// before the session becomes runnable again.
func (s *InfaiAgentSession) CompactChat(ctx context.Context) error {
	s.mu.Lock()
	if s.status != contracts.SessionIdle {
		s.mu.Unlock()
		return fmt.Errorf("session must be in idle state")
	}
	if !s.agent.MailboxEmpty() {
		s.mu.Unlock()
		return errors.New("session has queued messages")
	}
	s.mu.Unlock()

	trigger := "user triggered compaction"
	s.publish(contracts.EventStream{Kind: contracts.EventManualCompactionTriggered, Timestamp: time.Now().UTC(), Content: &trigger})

	replacement, summary, err := s.compactChat(ctx, false)
	if err != nil {
		if ctx.Err() == nil {
			reason := fmt.Sprintf("manual compaction: %s", err)
			s.publish(contracts.EventStream{Kind: contracts.EventSessionFatal, Timestamp: time.Now().UTC(), Content: &reason})
		}
		return err
	}
	if err := s.agent.ReplaceHistory(s.ctx, replacement); err != nil {
		return err
	}
	// Published after the install so the session is never reported runnable
	// while a stale history is still installed.
	if summary != "" {
		s.publish(contracts.EventStream{Kind: contracts.CompactionSummary, Timestamp: time.Now().UTC(), Content: &summary})
	}
	return nil
}

// autoCompact is the agent loop's mid-turn compaction callback. The loop owns
// its history, so this hands the continuation back instead of pushing it
// through ReplaceHistory.
func (s *InfaiAgentSession) autoCompact(ctx context.Context) ([]contracts.ChatMessage, error) {
	replacement, summary, err := s.compactChat(ctx, true)
	if err != nil {
		if ctx.Err() == nil {
			reason := fmt.Sprintf("automatic compaction: %s", err)
			s.publish(contracts.EventStream{Kind: contracts.EventSessionFatal, Timestamp: time.Now().UTC(), Content: &reason})
		}
		return nil, err
	}
	if summary != "" {
		s.publish(contracts.EventStream{Kind: contracts.CompactionSummary, Timestamp: time.Now().UTC(), Content: &summary})
	}
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
	// TODO: ask whether we need usage metrics from the compaction model?
	reply, _, err := s.model.Generate(ctx, messages, nil, &contracts.GenerateOptions{})
	if err != nil {
		return "", err
	}
	return reply.Text(), nil
}
