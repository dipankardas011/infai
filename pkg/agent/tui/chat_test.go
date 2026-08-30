package tui

import (
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

func TestTimelineTreeOrderPlacesBranchBelowParent(t *testing.T) {
	firstAssistant := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	mainUser := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	branchUser := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	branchReply := uuid.MustParse("00000000-0000-0000-0000-000000000005")

	selected := firstAssistant
	events := timelineTreeOrder([]TimelineEvent{
		{ID: branchReply, ParentID: branchUser},
		{ID: mainUser, ParentID: firstAssistant},
		{ID: branchUser, ParentID: firstAssistant, BranchFrom: &selected},
		{ID: firstAssistant, ParentID: uuid.Nil},
	})

	got := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		got = append(got, event.ID)
	}
	want := []uuid.UUID{firstAssistant, branchUser, branchReply, mainUser}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tree order=%v want=%v", got, want)
		}
	}
}

func TestBlocksFromRecordsShowsToolCallsAndResults(t *testing.T) {
	call := contracts.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: contracts.Function{
			Name:      "read",
			Arguments: `{"path":"README.md"}`,
		},
	}
	records := []store.Record{
		{Kind: store.KindMessage, Message: &contracts.ChatMessage{Role: "assistant", ToolCalls: []contracts.ToolCall{call}}},
		{Kind: store.KindToolResult, ToolResult: &store.ToolResultRecord{CallID: call.ID, Status: "success", Output: `{"content":"hello"}`}},
	}

	blocks := blocksFromRecords(records)
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d want 2: %#v", len(blocks), blocks)
	}
	if blocks[0].role != "tool" || blocks[0].text != `tool call: read {"path":"README.md"}` {
		t.Fatalf("tool call block=%#v", blocks[0])
	}
	if blocks[1].role != "tool" || blocks[1].text != "tool result: success\n{\"content\":\"hello\"}" {
		t.Fatalf("tool result block=%#v", blocks[1])
	}
}
