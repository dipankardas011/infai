package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestTaskChecklistOperationsReturnCompleteState(t *testing.T) {
	checklist := NewTaskChecklist()

	state := executeTaskChecklist(t, checklist, `{"action":"add","title":"Inspect lifecycle","description":"Trace resume and branch behavior.","status":"in_progress"}`)
	if len(state.Items) != 1 || state.Items[0].Status != contracts.TaskInProgress {
		t.Fatalf("add state = %+v", state)
	}
	state = executeTaskChecklist(t, checklist, `{"action":"update","title":"Inspect lifecycle","new_title":"Inspect session lifecycle","description":"Trace resume, compaction, and branches."}`)
	if len(state.Items) != 1 || state.Items[0].Title != "Inspect session lifecycle" {
		t.Fatalf("update state = %+v", state)
	}
	state = executeTaskChecklist(t, checklist, `{"action":"set_status","title":"Inspect session lifecycle","status":"completed"}`)
	if state.Items[0].Status != contracts.TaskCompleted {
		t.Fatalf("set_status state = %+v", state)
	}
	state = executeTaskChecklist(t, checklist, `{"action":"delete","title":"Inspect session lifecycle"}`)
	if state.Items == nil || len(state.Items) != 0 {
		t.Fatalf("delete state = %#v", state)
	}
}

func TestTaskChecklistDescriptionLimitUsesGraphemeClusters(t *testing.T) {
	checklist := NewTaskChecklist()
	cluster := "👨‍👩‍👧‍👦"

	description := strings.Repeat(cluster, 150)
	executeTaskChecklistArgs(t, checklist, map[string]any{
		"action": "add", "title": "Unicode task", "description": description,
	})

	tooLong := description + cluster
	_, err := executeTaskChecklistArgsError(checklist, map[string]any{
		"action": "update", "title": "Unicode task", "description": tooLong,
	})
	if err == nil || !strings.Contains(err.Error(), `item "Unicode task" description exceeds 150 characters`) {
		t.Fatalf("limit error = %v", err)
	}
	state := checklist.Snapshot()
	if state.Items[0].Description != description {
		t.Fatal("invalid update mutated checklist state")
	}
}

func TestTaskChecklistRejectsTwoInProgressItems(t *testing.T) {
	checklist := NewTaskChecklist()
	executeTaskChecklist(t, checklist, `{"action":"add","title":"First","description":"First task.","status":"in_progress"}`)
	_, err := executeTaskChecklistArgsError(checklist, map[string]any{
		"action": "add", "title": "Second", "description": "Second task.", "status": "in_progress",
	})
	if err == nil || !strings.Contains(err.Error(), "at most one in_progress") {
		t.Fatalf("in-progress error = %v", err)
	}
	if len(checklist.Snapshot().Items) != 1 {
		t.Fatal("invalid add mutated checklist state")
	}
}

func TestTaskChecklistRejectsFieldsForWrongAction(t *testing.T) {
	checklist := NewTaskChecklist()
	_, err := executeTaskChecklistArgsError(checklist, map[string]any{
		"action": "clear", "title": "must not be ignored",
	})
	if err == nil || !strings.Contains(err.Error(), "clear accepts only action") {
		t.Fatalf("clear validation error = %v", err)
	}
}

func executeTaskChecklist(t *testing.T, checklist *TaskChecklist, arguments string) contracts.TaskChecklistState {
	t.Helper()
	ctx := WithTaskChecklist(context.Background(), checklist)
	output, err := ExecuteMemoryToolCall(ctx, contracts.ToolCall{Function: contracts.Function{
		Name: string(contracts.TaskChecklistTool), Arguments: arguments,
	}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeTaskChecklistState(output)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func executeTaskChecklistArgs(t *testing.T, checklist *TaskChecklist, args map[string]any) contracts.TaskChecklistState {
	t.Helper()
	output, err := executeTaskChecklistArgsError(checklist, args)
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeTaskChecklistState(output)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func executeTaskChecklistArgsError(checklist *TaskChecklist, args map[string]any) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	ctx := WithTaskChecklist(context.Background(), checklist)
	return ExecuteMemoryToolCall(ctx, contracts.ToolCall{Function: contracts.Function{
		Name: string(contracts.TaskChecklistTool), Arguments: string(data),
	}})
}
