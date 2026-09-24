package session

import (
	"fmt"
	"slices"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/memory"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

func getLatestTaskChecklist(timeline *store.Timeline, head uuid.UUID) (contracts.TaskChecklistState, error) {
	state := contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{}}
	if head == uuid.Nil {
		return state, nil
	}
	events, err := timeline.LoadActiveContextAt(head)
	if err != nil {
		return state, err
	}
	records, err := CompleteResolveRawTimelineEventsToRecords(timeline, events)
	if err != nil {
		return state, err
	}

	toolResults := make(map[string]string)
	for _, record := range slices.Backward(records) {
		// It tries to look to reconstruct from the last known point from HEAD to parent/start of the timeline by traversing bottom to top.
		// start of the activeTimeline means it is either the first existing event aka the first user prompt or compacted message ;)
		// Bonous point is we already storing a metadata like taskchecklist in it under a xml or something like that.
		// So ordering of the switch case matters
		switch record.Kind {
		case store.KindMessage:
			if record.Message == nil {
				continue
			}
			message := record.Message
			if message.Role == "tool" {
				if message.Status != contracts.ToolExecutionSuccess {
					continue
				}
				toolResults[message.ToolCallID] = message.Text()
				continue
			}
			if message.Role != "assistant" {
				continue
			}

			for _, call := range slices.Backward(message.ToolCalls) {

				output, ok := toolResults[call.ID]
				if !ok {
					continue
				}
				delete(toolResults, call.ID)
				if call.Function.Name != contracts.TaskChecklistTool {
					continue
				}
				checklist, err := memory.DecodeTaskChecklistState(output)
				if err == nil {
					return checklist, nil
				}
			}

		case store.KindCompaction:
			if record.Compaction == nil || record.Compaction.TaskChecklist == nil {
				return state, nil
			}
			if err := memory.ValidateTaskChecklistState(*record.Compaction.TaskChecklist); err != nil {
				return state, fmt.Errorf("invalid compacted task checklist: %w", err)
			}
			return *record.Compaction.TaskChecklist, nil
		}
	}

	return state, nil
}
