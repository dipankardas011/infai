package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/clipperhouse/uax29/v2/graphemes"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const (
	maxTaskChecklistItems        = 100
	maxTaskTitleCharacters       = 200
	maxTaskDescriptionCharacters = 150
)

// TaskChecklist is one session's runtime projection. The session timeline
// remains the durable source of truth.
type TaskChecklist struct {
	mu    sync.RWMutex
	state contracts.TaskChecklistState
}

func NewTaskChecklist() *TaskChecklist {
	return &TaskChecklist{state: contracts.TaskChecklistState{Items: []contracts.TaskChecklistItem{}}}
}

func (c *TaskChecklist) Snapshot() contracts.TaskChecklistState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneTaskChecklistState(c.state)
}

func (c *TaskChecklist) Restore(state contracts.TaskChecklistState) error {
	if err := ValidateTaskChecklistState(state); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = cloneTaskChecklistState(state)
	return nil
}

func TaskChecklistTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.TaskChecklistTool),
		Description: "Manage your task checklist for this session. Keep titles short and descriptions concise but sufficient to resume the work. Actions: list; add requires title and description; update requires title and new_title and/or description; set_status requires title and status; delete requires title; clear removes everything. Every successful call returns the complete current checklist.",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"list", "add", "update", "set_status", "delete", "clear"},
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Item title, or the existing title for update, set_status, and delete",
				},
				"new_title": map[string]any{
					"type":        "string",
					"description": "Replacement title for update",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Task context of at most 150 Unicode grapheme clusters",
				},
				"status": map[string]any{
					"type": "string",
					"enum": []string{string(contracts.TaskPending), string(contracts.TaskInProgress), string(contracts.TaskCompleted)},
				},
			},
			RequiredFields:       []string{"action"},
			AdditionalProperties: false,
		},
	}
}

type taskChecklistArguments struct {
	Action      string                `json:"action"`
	Title       *string               `json:"title,omitempty"`
	NewTitle    *string               `json:"new_title,omitempty"`
	Description *string               `json:"description,omitempty"`
	Status      *contracts.TaskStatus `json:"status,omitempty"`
}

func taskChecklistExecution(ctx context.Context) (string, error) {
	checklist := TaskChecklistFromContext(ctx)
	if checklist == nil {
		return "", errors.New("task_checklist: no checklist manager in context")
	}
	call, ok := ToolCallFromContext(ctx)
	if !ok {
		return "", errors.New("task_checklist: no tool call in context")
	}

	var args taskChecklistArguments
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", fmt.Errorf("task_checklist arguments: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("task_checklist arguments must contain one JSON object")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	state, err := checklist.apply(args)
	if err != nil {
		return "", err
	}
	output, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode task_checklist state: %w", err)
	}
	return string(output), nil
}

func (c *TaskChecklist) apply(args taskChecklistArguments) (contracts.TaskChecklistState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := cloneTaskChecklistState(c.state)
	switch args.Action {
	case "list":
		if args.Title != nil || args.NewTitle != nil || args.Description != nil || args.Status != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist list accepts only action")
		}
	case "add":
		if args.Title == nil || strings.TrimSpace(*args.Title) == "" || args.Description == nil || strings.TrimSpace(*args.Description) == "" || args.NewTitle != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist add requires a non-empty title and description")
		}
		if taskItemIndex(state.Items, *args.Title) >= 0 {
			return contracts.TaskChecklistState{}, fmt.Errorf("task_checklist item %q already exists", *args.Title)
		}
		status := contracts.TaskPending
		if args.Status != nil {
			status = *args.Status
		}
		state.Items = append(state.Items, contracts.TaskChecklistItem{Title: *args.Title, Description: *args.Description, Status: status})
	case "update":
		if args.Title == nil || args.Status != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist update requires title and accepts only new_title and/or description")
		}
		index := taskItemIndex(state.Items, *args.Title)
		if index < 0 {
			return contracts.TaskChecklistState{}, fmt.Errorf("task_checklist item %q was not found", *args.Title)
		}
		if args.NewTitle == nil && args.Description == nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist update requires new_title or description")
		}
		if args.NewTitle != nil {
			if other := taskItemIndex(state.Items, *args.NewTitle); other >= 0 && other != index {
				return contracts.TaskChecklistState{}, fmt.Errorf("task_checklist item %q already exists", *args.NewTitle)
			}
			state.Items[index].Title = *args.NewTitle
		}
		if args.Description != nil {
			state.Items[index].Description = *args.Description
		}
	case "set_status":
		if args.Title == nil || args.Status == nil || args.NewTitle != nil || args.Description != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist set_status requires only title and status")
		}
		index := taskItemIndex(state.Items, *args.Title)
		if index < 0 {
			return contracts.TaskChecklistState{}, fmt.Errorf("task_checklist item %q was not found", *args.Title)
		}
		state.Items[index].Status = *args.Status
	case "delete":
		if args.Title == nil || args.NewTitle != nil || args.Description != nil || args.Status != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist delete requires only title")
		}
		index := taskItemIndex(state.Items, *args.Title)
		if index < 0 {
			return contracts.TaskChecklistState{}, fmt.Errorf("task_checklist item %q was not found", *args.Title)
		}
		state.Items = append(state.Items[:index], state.Items[index+1:]...)
	case "clear":
		if args.Title != nil || args.NewTitle != nil || args.Description != nil || args.Status != nil {
			return contracts.TaskChecklistState{}, errors.New("task_checklist clear accepts only action")
		}
		state.Items = []contracts.TaskChecklistItem{}
	default:
		return contracts.TaskChecklistState{}, fmt.Errorf("unknown task_checklist action %q", args.Action)
	}

	if err := ValidateTaskChecklistState(state); err != nil {
		return contracts.TaskChecklistState{}, err
	}
	c.state = cloneTaskChecklistState(state)
	return cloneTaskChecklistState(state), nil
}

func ValidateTaskChecklistState(state contracts.TaskChecklistState) error {
	if len(state.Items) > maxTaskChecklistItems {
		return fmt.Errorf("task_checklist cannot contain more than %d items", maxTaskChecklistItems)
	}
	seen := make(map[string]struct{}, len(state.Items))
	inProgress := 0
	for _, item := range state.Items {
		if strings.TrimSpace(item.Title) == "" {
			return errors.New("task_checklist item title cannot be empty")
		}
		if exceedsGraphemeLimit(item.Title, maxTaskTitleCharacters) {
			return fmt.Errorf("task_checklist item title %q exceeds %d characters", item.Title, maxTaskTitleCharacters)
		}
		if _, exists := seen[item.Title]; exists {
			return fmt.Errorf("task_checklist item %q appears more than once", item.Title)
		}
		seen[item.Title] = struct{}{}
		if strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("task_checklist item %q requires a non-empty description", item.Title)
		}
		if exceedsGraphemeLimit(item.Description, maxTaskDescriptionCharacters) {
			return fmt.Errorf("task_checklist item %q description exceeds %d characters", item.Title, maxTaskDescriptionCharacters)
		}
		switch item.Status {
		case contracts.TaskPending, contracts.TaskCompleted:
		case contracts.TaskInProgress:
			inProgress++
		default:
			return fmt.Errorf("task_checklist item %q has invalid status %q", item.Title, item.Status)
		}
	}
	if inProgress > 1 {
		return errors.New("task_checklist can have at most one in_progress item")
	}
	return nil
}

func DecodeTaskChecklistState(data string) (contracts.TaskChecklistState, error) {
	var state contracts.TaskChecklistState
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return contracts.TaskChecklistState{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return contracts.TaskChecklistState{}, errors.New("task_checklist state must contain one JSON object")
	}
	if err := ValidateTaskChecklistState(state); err != nil {
		return contracts.TaskChecklistState{}, err
	}
	return cloneTaskChecklistState(state), nil
}

func taskItemIndex(items []contracts.TaskChecklistItem, title string) int {
	for i := range items {
		if items[i].Title == title {
			return i
		}
	}
	return -1
}

func cloneTaskChecklistState(state contracts.TaskChecklistState) contracts.TaskChecklistState {
	state.Items = append([]contracts.TaskChecklistItem{}, state.Items...)
	return state
}

func exceedsGraphemeLimit(value string, limit int) bool {
	iterator := graphemes.FromString(value)
	count := 0
	for iterator.Next() {
		count++
		if count > limit {
			return true
		}
	}
	return false
}
