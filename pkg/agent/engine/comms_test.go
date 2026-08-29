package engine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestAgentCommsRoutesMessages(t *testing.T) {
	comms := NewAgentComms()
	agentID := uuid.New()
	if err := comms.RegisterAgent(agentID); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	t.Run("session to agent", func(t *testing.T) {
		want := AgentComm{To: agentID, Kind: AgentCommMessage}
		ctx := testContext(t)

		sent := make(chan error, 1)
		go func() {
			sent <- comms.SendToAgent(ctx, agentID, want)
		}()

		got, err := comms.ReceiveForAgent(ctx, agentID)
		if err != nil {
			t.Fatalf("receive from session: %v", err)
		}
		if err := <-sent; err != nil {
			t.Fatalf("send to agent: %v", err)
		}
		assert.Equal(t, want, got)
	})

	t.Run("agent to session", func(t *testing.T) {
		want := AgentComm{From: agentID, Kind: AgentCommResult}
		ctx := testContext(t)

		sent := make(chan error, 1)
		go func() {
			sent <- comms.SendToSession(ctx, want)
		}()

		got, err := comms.ReceiveFromAgents(ctx)
		if err != nil {
			t.Fatalf("receive from agent: %v", err)
		}
		if err := <-sent; err != nil {
			t.Fatalf("send to session: %v", err)
		}
		assert.Equal(t, want, got)
	})
}

func TestAgentCommsSharesSessionInbox(t *testing.T) {
	comms := NewAgentComms()
	firstID, secondID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{firstID, secondID} {
		if err := comms.RegisterAgent(id); err != nil {
			t.Fatalf("register agent %s: %v", id, err)
		}
	}

	ctx := testContext(t)
	for _, msg := range []AgentComm{
		{From: firstID, Kind: AgentCommResult},
		{From: secondID, Kind: AgentCommError},
	} {
		go func() { _ = comms.SendToSession(ctx, msg) }()
	}

	received := make(map[uuid.UUID]AgentCommKind, 2)
	for range 2 {
		msg, err := comms.ReceiveFromAgents(ctx)
		if err != nil {
			t.Fatalf("receive from session inbox: %v", err)
		}
		received[msg.From] = msg.Kind
	}

	want := map[uuid.UUID]AgentCommKind{
		firstID:  AgentCommResult,
		secondID: AgentCommError,
	}
	if len(received) != len(want) {
		t.Fatalf("received %#v, want %#v", received, want)
	}
	for id, kind := range want {
		if received[id] != kind {
			t.Fatalf("agent %s received kind %q, want %q", id, received[id], kind)
		}
	}
}

func TestAgentCommsClose(t *testing.T) {
	comms := NewAgentComms()
	comms.Close()

	if _, err := comms.ReceiveFromAgents(context.Background()); err != ErrAgentCommsClosed {
		t.Fatalf("receive error = %v, want %v", err, ErrAgentCommsClosed)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
