package delegate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/google/uuid"
)

func SpawnSidecarLoopTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.SpawnSidecarLoopTool),
		Description: "Start a self-contained task (no overlap among sidecars) for a sidecar agent and get its answer back as this call's result. The sidecar receives only one `task` you give it, cannot ask you questions and does not see this conversation. Use it when you need the answer before you can continue. Its a Foreground task handler.",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"agent_name": map[string]any{
					"type":        "string",
					"description": "Short name for the sidecar (at most 70 characters).",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "working directory for the sidecar loop/agent",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "The complete instruction for the sidecar. It sees nothing else and cannot ask you questions, so include everything it needs.",
				},
				"acceptance_script": map[string]any{
					"type":        "string",
					"description": "Bash script that must exit 0 for the work to count as done. Acts as a Eval function for the task and MUST BE READONLY it can perform build but no code writing or anything it just checks.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Turn budget for the sidecar; the engine clamps it.",
				},
			},
			RequiredFields:       []string{"agent_name", "task", "acceptance_script", "max_turns"},
			AdditionalProperties: false,
		},
	}
}

func SpawnBackgroundSidecarLoopTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.SpawnBackgroundSidecarLoopTool),
		Description: "Start a self-contained task (no overlap among sidecars) for a sidecar agent. Use it when you have other work to do while it runs. The sidecar receives only one `task` and cannot ask questions and does not see this conversation. Its a Background task delegation so sidecar will tell you when its done.",
		Parameters: contracts.ToolParameters{
			Type: "object",
			Properties: map[string]any{
				"agent_name": map[string]any{
					"type":        "string",
					"description": "Short name for the sidecar (at most 70 characters).",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "working directory for the sidecar loop/agent",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "The complete instruction for the sidecar. It sees nothing else and cannot ask you questions, so include everything it needs.",
				},
				"acceptance_script": map[string]any{
					"type":        "string",
					"description": "Bash script that must exit 0 for the work to count as done. Acts as a Eval function for the task and MUST BE READONLY it can perform build but no code writing or anything it just checks.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Turn budget for the sidecar; the engine clamps it.",
				},
			},
			RequiredFields:       []string{"agent_name", "task", "acceptance_script", "max_turns"},
			AdditionalProperties: false,
		},
	}
}

type Caller struct {
	SessionID uuid.UUID
	Comms     *comms.ISACChannel
	Cancelled <-chan struct{}
}

func RequestSidecar(ctx context.Context, caller Caller, call contracts.ToolCall) (uuid.UUID, error) {
	args, err := parseArgs(call)
	if err != nil {
		return uuid.Nil, err
	}

	decisions := make(chan comms.DelegationConformation, 1)
	unsubscribeDecision, err := caller.Comms.Subscribe(
		comms.AgentCommDelegationConformation,
		func(msg *comms.AgentComm) {
			var conformation comms.DelegationConformation
			if err := json.Unmarshal(msg.Payload, &conformation); err != nil {
				return
			}
			select {
			case decisions <- conformation:
			default:
			}
		},
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("listen for the engine's decision: %w", err)
	}
	defer unsubscribeDecision()

	request, err := json.Marshal(comms.DelegationToSidecarLoop{
		ParentID:         caller.SessionID,
		AgentName:        args.AgentName,
		Task:             args.Task,
		Cwd:              args.Cwd,
		AcceptanceScript: args.AcceptanceScript,
		MaxTurns:         args.MaxTurns,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode delegation: %w", err)
	}

	var requestKind comms.AgentCommKind
	switch call.Function.Name {
	case contracts.SpawnBackgroundSidecarLoopTool:
		requestKind = comms.AgentCommKindSpawnSidecarBackground
	case contracts.SpawnSidecarLoopTool:
		requestKind = comms.AgentCommKindSpawnSidecar
	}

	if err := caller.Comms.Send(ctx, &comms.AgentComm{
		From:    caller.SessionID,
		Kind:    requestKind,
		Payload: request,
	}); err != nil {
		return uuid.Nil, err
	}

	decision, err := awaitDecision(ctx, caller.Cancelled, decisions)
	if err != nil {
		return uuid.Nil, err
	}
	if decision.Err != "" {
		return uuid.Nil, fmt.Errorf("engine refused the delegation: %s", decision.Err)
	}

	return decision.DelegatedTo, nil
}

func awaitDecision(ctx context.Context, cancelled <-chan struct{}, decisions chan comms.DelegationConformation) (comms.DelegationConformation, error) {
	select {
	case decision := <-decisions:
		return decision, nil
	case <-ctx.Done():
		return comms.DelegationConformation{}, ctx.Err()
	case <-cancelled:
		return comms.DelegationConformation{}, harnessErr.ErrTurnCanceled
	}
}

func GraftedMessageForBackgroundSidecarLoop(sidecarID uuid.UUID) string {
	return fmt.Sprintf("sidecar agent `%s` started; its answer will arrive as a message from that agent", sidecarID)
}

func WaitForSidecars(ctx context.Context, caller Caller, sidecars []uuid.UUID, onAnswer func(comms.DelegatedTaskResponse)) (map[uuid.UUID]comms.DelegatedTaskResponse, error) {
	answers := make(map[uuid.UUID]comms.DelegatedTaskResponse, len(sidecars))
	if len(sidecars) == 0 {
		return answers, nil
	}

	waiting := make(map[uuid.UUID]struct{}, len(sidecars))
	for _, sidecarID := range sidecars {
		waiting[sidecarID] = struct{}{}
	}

	accumulated := make(chan comms.DelegatedTaskResponse, len(sidecars))
	unsubscribe, err := caller.Comms.Subscribe(comms.AgentCommKindResultSidecar, func(msg *comms.AgentComm) {
		var response comms.DelegatedTaskResponse
		if err := json.Unmarshal(msg.Payload, &response); err != nil {
			return
		}
		select {
		case accumulated <- response:
		default:
		}
	})
	if err != nil {
		return answers, fmt.Errorf("listen for sidecar answers: %w", err)
	}
	defer unsubscribe()

	for len(waiting) > 0 {
		select {
		case response := <-accumulated:
			if _, expected := waiting[response.From]; !expected {
				// Not this batch's, or already collected: an answer a call that
				// gave up left behind on the kind.
				continue
			}
			delete(waiting, response.From)
			answers[response.From] = response
			onAnswer(response)
		case <-ctx.Done():
			return answers, ctx.Err()
		case <-caller.Cancelled:
			return answers, harnessErr.ErrTurnCanceled
		}
	}

	return answers, nil
}

// Answer is what a foreground call returns to the model.
func Answer(response comms.DelegatedTaskResponse) (string, error) {
	if strings.TrimSpace(response.Error) != "" {
		return "", fmt.Errorf("sidecar %s ended %s: %s", response.From, response.Status, response.Error)
	}
	summary := strings.TrimSpace(response.Summary)
	if summary == "" {
		return "", fmt.Errorf("sidecar %s ended %s without an answer", response.From, response.Status)
	}
	return summary, nil
}

func AnswerText(response comms.DelegatedTaskResponse) string {
	switch {
	case strings.TrimSpace(response.Error) != "":
		return fmt.Sprintf("[sidecar %s %s] %s", response.From, response.Status, response.Error)
	case strings.TrimSpace(response.Summary) == "":
		return fmt.Sprintf("[sidecar %s %s] no answer", response.From, response.Status)
	default:
		return fmt.Sprintf("[sidecar %s %s]\n%s", response.From, response.Status, strings.TrimSpace(response.Summary))
	}
}

type spawnArgs struct {
	AgentName        string `json:"agent_name"`
	Task             string `json:"task"`
	Cwd              string `json:"cwd"`
	AcceptanceScript string `json:"acceptance_script"`
	MaxTurns         uint64 `json:"max_turns"`
}

func parseArgs(call contracts.ToolCall) (spawnArgs, error) {
	args, err := contracts.DecodeToolArguments[spawnArgs](call.Function.Name, call)
	if err != nil {
		return args, err
	}
	args.AgentName = strings.TrimSpace(args.AgentName)
	if args.AgentName == "" || utf8.RuneCountInString(args.AgentName) > 70 {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "agent_name must be 1 to 70 characters", contracts.ResponsibilityAgent, nil)
	}
	if strings.TrimSpace(args.Task) == "" || strings.TrimSpace(args.AcceptanceScript) == "" || args.MaxTurns == 0 {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "task, acceptance_script, and max_turns (greater than zero) are required", contracts.ResponsibilityAgent, nil)
	}
	if !utf8.ValidString(args.AcceptanceScript) || strings.ContainsRune(args.AcceptanceScript, '\x00') || strings.ContainsRune(args.AcceptanceScript, '\r') {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "acceptance_script must be valid UTF-8 bash source without NUL or carriage returns", contracts.ResponsibilityAgent, nil)
	}
	return args, nil
}
