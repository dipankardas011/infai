package comms

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestAgentCommsRoutesMessages(t *testing.T) {
	hub := NewAgentComms()
	t.Cleanup(hub.Close)

	agentID := uuid.New()
	if err := hub.RegisterSessionAgent(agentID); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	iac := hub.NewSessionAgentComms(agentID)

	t.Run("engine to agent", func(t *testing.T) {
		inbound := subscribeInbox(t, iac, AgentCommKindResultSidecar)
		want := &AgentComm{To: agentID, Kind: AgentCommKindResultSidecar}
		ctx := testContext(t)

		if err := hub.SendToSessionAgent(ctx, agentID, want); err != nil {
			t.Fatalf("send to agent: %v", err)
		}

		select {
		case got := <-inbound:
			assert.Equal(t, want, got)
		case <-ctx.Done():
			t.Fatal("agent inbox received nothing")
		}
	})

	t.Run("agent to engine", func(t *testing.T) {
		want := &AgentComm{From: agentID, Kind: AgentCommKindResultSidecar}
		ctx := testContext(t)

		if err := iac.Send(ctx, want); err != nil {
			t.Fatalf("send to engine: %v", err)
		}

		got, err := hub.SubscribeForSessionAgentsEvents(ctx)
		if err != nil {
			t.Fatalf("receive from engine inbox: %v", err)
		}
		assert.Equal(t, want, got)
	})
}

func TestAgentCommsSharesEngineInbox(t *testing.T) {
	hub := NewAgentComms()
	t.Cleanup(hub.Close)

	firstID, secondID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{firstID, secondID} {
		if err := hub.RegisterSessionAgent(id); err != nil {
			t.Fatalf("register agent %s: %v", id, err)
		}
	}

	ctx := testContext(t)
	for _, id := range []uuid.UUID{firstID, secondID} {
		if err := hub.NewSessionAgentComms(id).Send(ctx, &AgentComm{From: id, Kind: AgentCommKindResultSidecar}); err != nil {
			t.Fatalf("agent %s send to engine: %v", id, err)
		}
	}

	received := make(map[uuid.UUID]AgentCommKind, 2)
	for range 2 {
		msg, err := hub.SubscribeForSessionAgentsEvents(ctx)
		if err != nil {
			t.Fatalf("receive from engine inbox: %v", err)
		}
		received[msg.From] = msg.Kind
	}

	want := map[uuid.UUID]AgentCommKind{
		firstID:  AgentCommKindResultSidecar,
		secondID: AgentCommKindResultSidecar,
	}
	if len(received) != len(want) {
		t.Fatalf("received %#v, want %#v", received, want)
	}
	for id, kind := range want {
		if received[id] != kind {
			t.Fatalf("message from %s had kind %q, want %q", id, received[id], kind)
		}
	}
}

func TestAgentCommsRejectsUnknownAgent(t *testing.T) {
	hub := NewAgentComms()
	t.Cleanup(hub.Close)

	agentID := uuid.New()
	iac := hub.NewSessionAgentComms(agentID)
	ctx := testContext(t)

	if err := hub.SendToSessionAgent(ctx, agentID, &AgentComm{To: agentID, Kind: AgentCommKindResultSidecar}); err != ErrAgentNotRegistered {
		t.Fatalf("send to unregistered agent error = %v, want %v", err, ErrAgentNotRegistered)
	}
	if _, err := iac.Subscribe(AgentCommKindResultSidecar, func(*AgentComm) {}); err != ErrAgentNotRegistered {
		t.Fatalf("subscribe on unregistered agent error = %v, want %v", err, ErrAgentNotRegistered)
	}

	if err := hub.RegisterSessionAgent(agentID); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	hub.UnregisterSessionAgent(agentID)

	if err := hub.SendToSessionAgent(ctx, agentID, &AgentComm{To: agentID, Kind: AgentCommKindResultSidecar}); err != ErrAgentNotRegistered {
		t.Fatalf("send to unregistered agent error = %v, want %v", err, ErrAgentNotRegistered)
	}
}

func TestAgentCommsClose(t *testing.T) {
	hub := NewAgentComms()
	hub.Close()

	if _, err := hub.SubscribeForSessionAgentsEvents(context.Background()); err != ErrAgentCommsClosed {
		t.Fatalf("receive error = %v, want %v", err, ErrAgentCommsClosed)
	}
}

// The delegation round trip the spawn tools will use, in the order the four
// kinds carry it: the caller asks for a sidecar, the engine answers with the
// sidecar's own AgentID on the conformation kind, and the sidecar's outcome
// arrives later on the result kind carrying that same id. Nothing in the
// request identifies it — the engine's answer is what creates the identity.
func TestDelegationRoundTrip(t *testing.T) {
	hub := NewAgentComms()
	t.Cleanup(hub.Close)

	callerID := uuid.New()
	if err := hub.RegisterSessionAgent(callerID); err != nil {
		t.Fatalf("register caller: %v", err)
	}
	caller := hub.NewSessionAgentComms(callerID)

	conformations := make(chan DelegationConformation, 1)
	unsubscribeConformations, err := caller.Subscribe(AgentCommDelegationConformation, func(msg *AgentComm) {
		var conformation DelegationConformation
		if err := json.Unmarshal(msg.Payload, &conformation); err != nil {
			return
		}
		conformations <- conformation
	})
	if err != nil {
		t.Fatalf("subscribe for conformations: %v", err)
	}
	t.Cleanup(unsubscribeConformations)

	results := make(chan DelegatedTaskResponse, 1)
	unsubscribeResults, err := caller.Subscribe(AgentCommKindResultSidecar, func(msg *AgentComm) {
		var response DelegatedTaskResponse
		if err := json.Unmarshal(msg.Payload, &response); err != nil {
			return
		}
		results <- response
	})
	if err != nil {
		t.Fatalf("subscribe for results: %v", err)
	}
	t.Cleanup(unsubscribeResults)

	ctx := testContext(t)
	request, err := json.Marshal(DelegationToSidecarLoop{ParentID: callerID, Task: "check the thing"})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if err := caller.Send(ctx, &AgentComm{
		From:    callerID,
		Kind:    AgentCommKindSpawnSidecar,
		Payload: request,
	}); err != nil {
		t.Fatalf("send spawn request: %v", err)
	}

	// The engine reads one inbox, so the kind is what tells it what arrived.
	msg, err := hub.SubscribeForSessionAgentsEvents(ctx)
	if err != nil {
		t.Fatalf("read engine inbox: %v", err)
	}
	assert.Equal(t, AgentCommKindSpawnSidecar, msg.Kind)

	var read DelegationToSidecarLoop
	if err := json.Unmarshal(msg.Payload, &read); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	assert.Equal(t, callerID, read.ParentID)
	assert.Equal(t, "check the thing", read.Task)

	// It answers with the sidecar it created, addressed to whoever asked.
	sidecarID := uuid.New()
	conformation, err := json.Marshal(DelegationConformation{DelegatedTo: sidecarID})
	if err != nil {
		t.Fatalf("encode conformation: %v", err)
	}
	if err := hub.SendToSessionAgent(ctx, msg.From, &AgentComm{
		From:    sidecarID,
		To:      callerID,
		Kind:    AgentCommDelegationConformation,
		Payload: conformation,
	}); err != nil {
		t.Fatalf("send conformation: %v", err)
	}

	select {
	case got := <-conformations:
		assert.Equal(t, sidecarID, got.DelegatedTo)
		assert.Empty(t, got.Err)
	case <-ctx.Done():
		t.Fatal("the caller received no conformation")
	}

	// The sidecar finishes, and its outcome names it, so the caller knows which
	// delegation it settles without the request having carried an id.
	response, err := json.Marshal(DelegatedTaskResponse{
		From:    sidecarID,
		Status:  contracts.SessionCompleted,
		Summary: "the answer",
	})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	if err := hub.SendToSessionAgent(ctx, callerID, &AgentComm{
		From:    sidecarID,
		To:      callerID,
		Kind:    AgentCommKindResultSidecar,
		Payload: response,
	}); err != nil {
		t.Fatalf("send result: %v", err)
	}

	select {
	case got := <-results:
		assert.Equal(t, sidecarID, got.From)
		assert.Equal(t, contracts.SessionCompleted, got.Status)
		assert.Equal(t, "the answer", got.Summary)
	case <-ctx.Done():
		t.Fatal("the caller received no result")
	}
}

// subscribeInbox attaches a handler that forwards one message of kind, so a
// test can assert on agent-bound delivery the way an agent would receive it.
func subscribeInbox(t *testing.T, iac *ISACChannel, kind AgentCommKind) <-chan *AgentComm {
	t.Helper()

	inbound := make(chan *AgentComm, 1)
	unsubscribe, err := iac.Subscribe(kind, func(msg *AgentComm) { inbound <- msg })
	if err != nil {
		t.Fatalf("subscribe to %q: %v", kind, err)
	}
	t.Cleanup(unsubscribe)
	return inbound
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
