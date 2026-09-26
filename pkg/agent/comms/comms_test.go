package comms

import (
	"context"
	"testing"
	"time"

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
		inbound := subscribeInbox(t, iac, AgentCommKindSubagent)
		want := &AgentComm{To: agentID, Kind: AgentCommKindSubagent}
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
		want := &AgentComm{From: agentID, Kind: AgentCommKindSubagent}
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
		if err := hub.NewSessionAgentComms(id).Send(ctx, &AgentComm{From: id, Kind: AgentCommKindSubagent}); err != nil {
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
		firstID:  AgentCommKindSubagent,
		secondID: AgentCommKindSubagent,
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

	if err := hub.SendToSessionAgent(ctx, agentID, &AgentComm{To: agentID, Kind: AgentCommKindSubagent}); err != ErrAgentNotRegistered {
		t.Fatalf("send to unregistered agent error = %v, want %v", err, ErrAgentNotRegistered)
	}
	if _, err := iac.Subscribe(AgentCommKindSubagent, func(*AgentComm) {}); err != ErrAgentNotRegistered {
		t.Fatalf("subscribe on unregistered agent error = %v, want %v", err, ErrAgentNotRegistered)
	}

	if err := hub.RegisterSessionAgent(agentID); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	hub.UnregisterSessionAgent(agentID)

	if err := hub.SendToSessionAgent(ctx, agentID, &AgentComm{To: agentID, Kind: AgentCommKindSubagent}); err != ErrAgentNotRegistered {
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
