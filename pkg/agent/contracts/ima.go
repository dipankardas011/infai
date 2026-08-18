package contracts

import (
	"context"
	"time"
)

// AgentEngine (Domain Logic)
//     ├── Injected: IModelAdapter
//     ├── Injected: IToolExecutor
//     └── Injected: IContextStore
//
// Method: Step()
//     1. context  := ContextStore.BuildCurrentWindow()
//     2. response := ModelAdapter.Generate(context)
//     3. actions  := Parser.ExtractToolCalls(response)
//     4. IF len(actions) == 0: RETURN StopSignal
//     5. results  := ToolExecutor.ExecuteAll(actions)
//     6. ContextStore.AppendResults(results)
//     7. REPEAT

type InfaiModelAdaptor interface {
	// llm handles the organise with the specific provider needs
	InitializeSession(systemPrompt string, tools map[string]string, skills map[string]string, mcps map[string]string, memories map[string]string) error

	EstablishRESTConnection(ctx context.Context, MaxBackoffRetryDuringNetworkError int) error
	// EstablishWebSocetConnection(ctx context.Context, MaxBackoffRetryDuringNetworkError int) error

	ListModels(ctx context.Context, ttl time.Duration) ([]string, error)
	AvailableThinkingModes() ([]string, error)
	Generate() error
	CurrentCost() (<-chan float64, error)
}

// ChatCompletion()
