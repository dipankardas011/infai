package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
	"github.com/google/uuid"
)

func DelegateSidecarLoopTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.DelegateSidecarLoopTool),
		Description: "Delegate a self-contained task and wait for its result. Include a read-only, task-specific acceptance script; report the exact failed check and reason if it fails.",
		Parameters: contracts.ToolParameterObjectSchema(map[string]any{
			"agent_name": map[string]any{
				"type":        "string",
				"description": "Short name for the sidecar you will need to remember it (at most 70 characters).",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "working directory for the sidecar; defaults to this session's when left empty",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "The complete instruction for the sidecar: the goal, the files or areas to work on, the definition of done, and the command that must pass. It sees nothing else, cannot ask questions and does not see this conversation, so include everything it needs.",
			},
			"acceptance_script": map[string]any{
				"type":        "string",
				"description": "Read-only bash script that must exit 0 for the work to count as done; it may build and inspect, never write. Echo why it failed — the sidecar is shown what it prints, not just the exit status.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"enum":        []int{20, 40, 100},
				"description": "Maximum model generations for the sidecar: a tool call uses one generation, and another is needed to inspect its result. 20 for a lookup or two, 40 for a multi-step task, 100 for a tree-wide change.",
			},
		}, []string{"agent_name", "task", "acceptance_script", "max_turns"}),
	}
}

func DelegateBackgroundSidecarLoopTool() contracts.Tool {
	return contracts.Tool{
		Name:        string(contracts.DelegateBackgroundSidecarLoopTool),
		Description: "Delegate a self-contained task while you do other work; its result arrives asynchronously. Include a read-only, task-specific acceptance script; report the exact failed check and reason if it fails.",
		Parameters: contracts.ToolParameterObjectSchema(map[string]any{
			"agent_name": map[string]any{
				"type":        "string",
				"description": "Short name for the sidecar you will need to remember it (at most 70 characters).",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "working directory for the sidecar; defaults to this session's when left empty",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "The complete instruction for the sidecar: the goal, the files or areas to work on, the definition of done, and the command that must pass. It sees nothing else, cannot ask questions and does not see this conversation, so include everything it needs.",
			},
			"acceptance_script": map[string]any{
				"type":        "string",
				"description": "Read-only bash script that must exit 0 for the work to count as done; it may build and inspect, never write. Echo why it failed — the sidecar is shown what it prints, not just the exit status.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"enum":        []int{20, 40, 100},
				"description": "Maximum model generations for the sidecar: a tool call uses one generation, and another is needed to inspect its result. 20 for a lookup or two, 40 for a multi-step task, 100 for a tree-wide change.",
			},
		}, []string{"agent_name", "task", "acceptance_script", "max_turns"}),
	}
}

type Caller struct {
	SessionID uuid.UUID
	Comms     *comms.ISACChannel
	Cancelled <-chan struct{}
}

func RequestSidecar(ctx context.Context, caller Caller, call contracts.ToolCall) (uuid.UUID, string, error) {
	args, err := parseArgs(call)
	if err != nil {
		return uuid.Nil, "", err
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
		return uuid.Nil, "", fmt.Errorf("listen for the engine's decision: %w", err)
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
		return uuid.Nil, "", fmt.Errorf("encode delegation: %w", err)
	}

	var requestKind comms.AgentCommKind
	switch call.Function.Name {
	case contracts.DelegateBackgroundSidecarLoopTool:
		requestKind = comms.AgentCommKindDelegateSidecarBackground
	case contracts.DelegateSidecarLoopTool:
		requestKind = comms.AgentCommKindDelegateSidecar
	}

	if err := caller.Comms.Send(ctx, &comms.AgentComm{
		From:    caller.SessionID,
		Kind:    requestKind,
		Payload: request,
	}); err != nil {
		return uuid.Nil, "", err
	}

	decision, err := awaitDecision(ctx, caller.Cancelled, decisions)
	if err != nil {
		return uuid.Nil, "", err
	}
	if decision.Err != "" {
		return uuid.Nil, "", fmt.Errorf("engine refused the delegation: %s", decision.Err)
	}

	return decision.DelegatedTo, args.AgentName, nil
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

func GraftedMessageForBackgroundSidecarLoop(name string, sidecarID uuid.UUID) string {
	return fmt.Sprintf("sidecar agent %q (%s) started; its answer will arrive as a message from that agent", name, sidecarID)
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

func SidecarAttribution(name string, id uuid.UUID) string {
	if strings.TrimSpace(name) == "" {
		return id.String()
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

func SidecarLoopRespAsToolCallResult(response comms.DelegatedTaskResponse) (string, error) {
	text := sidecarAnswerText(response)
	if strings.TrimSpace(response.Error) != "" {
		return "", errors.New(text)
	}
	return text, nil
}

// From Bg sidecar to status updates for the parent session.
func SidecarLoopRespAsNonToolCallResult(response comms.DelegatedTaskResponse) string {
	return sidecarAnswerText(response)
}

func sidecarAnswerText(response comms.DelegatedTaskResponse) string {
	label := SidecarAttribution(response.Name, response.From)
	header := fmt.Sprintf("[sidecar_loop %s %s]", label, response.Status)

	lines := []string{header}
	if line := acceptanceLine(response.Eval); line != "" {
		lines = append(lines, line)
	}
	if errorText := strings.TrimSpace(response.Error); errorText != "" {
		lines = append(lines, errorText)
		if summary := strings.TrimSpace(response.Summary); summary != "" {
			lines = append(lines, "last answer:\n"+summary)
		}
	} else if summary := strings.TrimSpace(response.Summary); summary != "" {
		lines = append(lines, summary)
	} else {
		lines = append(lines, "no answer")
	}
	if checklist := formatTaskChecklist(response.TaskChecklist); checklist != "" {
		lines = append(lines, checklist)
	}
	return strings.Join(lines, "\n")
}

func acceptanceLine(eval *contracts.EvalResult) string {
	switch {
	case eval == nil:
		return ""
	case eval.Passed:
		return contracts.AcceptanceScriptPrefix + "success]"
	default:
		return contracts.AcceptanceScriptPrefix + "failure]"
	}
}

func formatTaskChecklist(state *contracts.TaskChecklistState) string {
	if state == nil || len(state.Items) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("Task checklist:")
	for _, item := range state.Items {
		fmt.Fprintf(&out, "\n- [%s] %s", item.Status, item.Title)
		if description := strings.TrimSpace(item.Description); description != "" {
			out.WriteString(": ")
			out.WriteString(description)
		}
	}
	return out.String()
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
	if strings.TrimSpace(args.Task) == "" || strings.TrimSpace(args.AcceptanceScript) == "" {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "task and acceptance_script are required", contracts.ResponsibilityAgent, nil)
	}
	if args.MaxTurns != 20 && args.MaxTurns != 40 && args.MaxTurns != 100 {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "max_turns must be 20, 40, or 100", contracts.ResponsibilityAgent, nil)
	}
	if !utf8.ValidString(args.AcceptanceScript) || strings.ContainsRune(args.AcceptanceScript, '\x00') || strings.ContainsRune(args.AcceptanceScript, '\r') {
		return args, contracts.NewToolExecutionError(call.Function.Name, "invalid_arguments", "acceptance_script must be valid UTF-8 bash source without NUL or carriage returns", contracts.ResponsibilityAgent, nil)
	}
	return args, nil
}
