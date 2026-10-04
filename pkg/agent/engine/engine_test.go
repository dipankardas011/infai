package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/session"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// A status reaches the parent that delegated, and only when it comes from a
// session the engine actually recorded as that parent's child.
func TestEngineRelaysSidecarStatusOnlyFromAChild(t *testing.T) {
	hub := comms.NewAgentComms()
	parentID, childID, strangerID := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{parentID, childID, strangerID} {
		if err := hub.RegisterSessionAgent(id); err != nil {
			t.Fatal(err)
		}
	}
	e := &InfaiAgentEngine{
		ctx:                 context.Background(),
		bgLogger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		aseComms:            hub,
		activeSessionAgents: map[uuid.UUID]*session.InfaiAgentSession{parentID: nil},
		children: map[uuid.UUID]map[uuid.UUID]comms.AgentCommKind{
			parentID: {childID: comms.AgentCommKindSpawnSidecar},
		},
	}

	received := make(chan contracts.SidecarStatus, 4)
	unsubscribe, err := hub.NewSessionAgentComms(parentID).Subscribe(
		comms.AgentCommKindSidecarStatus,
		func(ac *comms.AgentComm) {
			var status contracts.SidecarStatus
			if json.Unmarshal(ac.Payload, &status) == nil {
				received <- status
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	payload, err := json.Marshal(contracts.SidecarStatus{ID: childID, Name: "worker", Status: contracts.SessionBusy})
	if err != nil {
		t.Fatal(err)
	}
	e.relaySidecarStatus(&comms.AgentComm{From: childID, To: parentID, Kind: comms.AgentCommKindSidecarStatus, Payload: payload})

	select {
	case got := <-received:
		if got.ID != childID || got.Status != contracts.SessionBusy || got.Name != "worker" {
			t.Fatalf("parent received %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the parent never received its child's status")
	}

	// A session that is not this parent's child is a stranger, and its report is
	// dropped rather than delivered.
	e.relaySidecarStatus(&comms.AgentComm{From: strangerID, To: parentID, Kind: comms.AgentCommKindSidecarStatus, Payload: payload})
	select {
	case got := <-received:
		t.Fatalf("a stranger's report was relayed: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// A child replaces its own row with each report, so the reports must reach the
// parent in the order the child sent them. A relay that hands each one to its
// own goroutine lets the terminal report overtake the one before it, which
// leaves the caller's row on a status the child has already left.
func TestSidecarStatusReportsKeepTheirOrder(t *testing.T) {
	hub := comms.NewAgentComms()
	parentID, childID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{parentID, childID} {
		if err := hub.RegisterSessionAgent(id); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &InfaiAgentEngine{
		ctx:                 ctx,
		bgLogger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		aseComms:            hub,
		activeSessionAgents: map[uuid.UUID]*session.InfaiAgentSession{parentID: nil},
		children: map[uuid.UUID]map[uuid.UUID]comms.AgentCommKind{
			parentID: {childID: comms.AgentCommKindSpawnSidecar},
		},
	}
	go e.listenForAgentComms()

	received := make(chan contracts.SessionStatus, 64)
	unsubscribe, err := hub.NewSessionAgentComms(parentID).Subscribe(
		comms.AgentCommKindSidecarStatus,
		func(ac *comms.AgentComm) {
			var status contracts.SidecarStatus
			if json.Unmarshal(ac.Payload, &status) == nil {
				received <- status.Status
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	want := []contracts.SessionStatus{
		contracts.SessionBusy,
		contracts.SessionWaitingApproval,
		contracts.SessionBusy,
		contracts.SessionCompacting,
		contracts.SessionCompleted,
	}
	sender := hub.NewSessionAgentComms(childID)
	for _, status := range want {
		payload, err := json.Marshal(contracts.SidecarStatus{ID: childID, Name: "worker", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if err := sender.Send(ctx, &comms.AgentComm{
			From: childID, To: parentID, Kind: comms.AgentCommKindSidecarStatus, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for i, wantStatus := range want {
		select {
		case got := <-received:
			if got != wantStatus {
				t.Fatalf("report %d arrived as %q, want %q", i, got, wantStatus)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("report %d never arrived", i)
		}
	}
}

func TestSidecarNameIsPersistedOnCreation(t *testing.T) {
	ss, err := store.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := contracts.LLMModelConfiguration{Id: "test-model", MaxContextLength: 1000}
	provider := contracts.LLMProviderConfiguration{
		Id: contracts.OpenAIGeneric, BaseEndpoint: "http://127.0.0.1:1", APIType: contracts.OpenAICompatableAPI,
		Auth: contracts.LLMProviderAuth{Method: contracts.NoneAuth}, Models: map[string]contracts.LLMModelConfiguration{model.Id: model},
	}
	hub := comms.NewAgentComms()
	parentID := uuid.New()
	if err := hub.RegisterSessionAgent(parentID); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &InfaiAgentEngine{
		ctx: context.Background(), bgLogger: logger, sessionStore: ss, aseComms: hub,
		providers:           contracts.LLMProviders{Providers: map[string]contracts.LLMProviderConfiguration{"test": provider}},
		activeSessionAgents: make(map[uuid.UUID]*session.InfaiAgentSession),
		children:            make(map[uuid.UUID]map[uuid.UUID]comms.AgentCommKind), stopCh: make(chan struct{}),
	}
	// An interactive session carries no turn limit: the engine passes nil for
	// one, and the loop falls back to the session default.
	parent, err := session.NewSession(e.ctx, parentID, uuid.Nil, "", logger, nil, nil, nil,
		contracts.NewProvisionedModel(provider.Id, "test", provider.BaseEndpoint, provider.APIType, provider.Auth, model),
		t.TempDir(), ss, hub.NewSessionAgentComms(parentID), contracts.InteractiveAgent)
	if err != nil {
		t.Fatal(err)
	}
	e.activeSessionAgents[parentID] = parent
	defer e.CloseSession(parentID)

	childID, err := e.createSidecarSession(parentID, comms.DelegationToSidecarLoop{
		ParentID: parentID, AgentName: "Build worker", Task: "check build", AcceptanceScript: "true", MaxTurns: 20,
	}, comms.AgentCommKindSpawnSidecar)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ss.LoadMeta(childID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "Build worker" || meta.ParentID != parentID {
		t.Fatalf("persisted child identity = (%q, %s)", meta.Name, meta.ParentID)
	}
}
