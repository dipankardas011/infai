package engine

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/session"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

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
