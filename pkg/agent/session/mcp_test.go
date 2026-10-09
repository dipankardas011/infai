package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/comms"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSessionMCPServerProcess(t *testing.T) {
	if os.Getenv("INFAI_SESSION_MCP_TEST_SERVER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "session-test", Version: "1"}, nil)
	var calls atomic.Int32
	mcp.AddTool(server, &mcp.Tool{
		Name: "probe", Description: "session integration probe",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		Mode   string `json:"mode"`
		Marker string `json:"marker"`
	}) (*mcp.CallToolResult, any, error) {
		count := calls.Add(1)
		if input.Marker != "" {
			if err := os.WriteFile(input.Marker, []byte("started"), 0o600); err != nil {
				return nil, nil, err
			}
		}
		if input.Mode == "block" {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		if input.Mode == "error" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "probe failed"}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("call=%d", count)}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func configureSessionMCPTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := contracts.ToolsConfig{MCPServers: map[string]contracts.MCPServerConfig{
		"local": {
			Transport: "stdio", Command: executable,
			Args: []string{"-test.run=^TestSessionMCPServerProcess$"},
			Env:  map[string]string{"INFAI_SESSION_MCP_TEST_SERVER": "1"}, TimeoutSeconds: 5,
		},
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Root()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newMCPRuntimeTestSession(t *testing.T, kind contracts.AgentKind) *InfaiAgentSession {
	t.Helper()
	base := newBareRuntimeSession(t, nil, nil, nil)
	base.cancel(nil)
	base.meta.AgentKind = kind
	base.agentMailbox.PreventDraining()
	hub := comms.NewAgentComms()
	t.Cleanup(hub.Close)
	if err := hub.RegisterSessionAgent(base.meta.ID); err != nil {
		t.Fatal(err)
	}
	s, err := newRuntimeSession(context.Background(), base.l, base.model, base.meta, nil, base.timeline, base.store, base.agentMailbox, hub.NewSessionAgentComms(base.meta.ID), nil)
	if err != nil {
		_ = base.timeline.Close()
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func sessionMCPTestCall(t *testing.T, s *InfaiAgentSession, id, mode, marker string) contracts.ToolCall {
	t.Helper()
	tools := s.mcpManager.Tools()
	if len(tools) != 1 {
		t.Fatalf("MCP tools = %d, want 1", len(tools))
	}
	name := contracts.ToolType(tools[0].Name)
	if got := s.auditorPolicy.Check(name); got != auditor.HumanPolicy {
		t.Fatalf("MCP policy = %v, want HumanPolicy despite read-only annotation", got)
	}
	found := false
	for _, tool := range s.availableTools {
		if tool.Name == string(name) {
			found = true
		}
	}
	if !found {
		t.Fatal("MCP tool not registered in session")
	}
	arguments, err := json.Marshal(map[string]string{"mode": mode, "marker": marker})
	if err != nil {
		t.Fatal(err)
	}
	return contracts.ToolCall{ID: id, Function: contracts.Function{Name: name, Arguments: string(arguments)}}
}

type sessionMCPDispatchResult struct {
	messages []contracts.ChatMessage
	canceled bool
}

func dispatchSessionMCP(s *InfaiAgentSession, calls ...contracts.ToolCall) <-chan sessionMCPDispatchResult {
	done := make(chan sessionMCPDispatchResult, 1)
	go func() {
		messages, canceled := s.GenToolCallDispatchHandler()(calls)
		done <- sessionMCPDispatchResult{messages, canceled}
	}()
	return done
}

func resolveSessionMCPApproval(t *testing.T, s *InfaiAgentSession, decision contracts.ApprovalDecision) {
	t.Helper()
	waitForSessionStatus(t, s, contracts.SessionWaitingApproval)
	view, _, unsubscribe, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if view.PendingApproval == nil {
		t.Fatal("MCP pending approval missing")
	}
	request := view.PendingApproval
	if err := s.ResolveApproval(request.ID, contracts.ApprovalConclusion{ReqID: request.ID, Fingerprint: request.Fingerprint, Decision: decision}); err != nil {
		t.Fatal(err)
	}
}

func receiveSessionMCPDispatch(t *testing.T, done <-chan sessionMCPDispatchResult) sessionMCPDispatchResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("MCP dispatch did not finish")
		return sessionMCPDispatchResult{}
	}
}

func TestSessionMCPApprovalAndExecution(t *testing.T) {
	configureSessionMCPTest(t)
	for _, kind := range []contracts.AgentKind{contracts.InteractiveAgent, contracts.SingleLoopAgent, contracts.SidecarLoopAgent} {
		t.Run(string(kind), func(t *testing.T) {
			s := newMCPRuntimeTestSession(t, kind)
			marker := filepath.Join(t.TempDir(), "executed")
			call := sessionMCPTestCall(t, s, "denied", "", marker)
			if kind != contracts.InteractiveAgent {
				// Every kind must connect and discover; approval and dispatch are
				// shared code, so only the first kind runs them.
				return
			}
			done := dispatchSessionMCP(s, call)
			resolveSessionMCPApproval(t, s, contracts.ApprovalDeny)
			result := receiveSessionMCPDispatch(t, done)
			if result.canceled || len(result.messages) != 1 || result.messages[0].Status != contracts.ToolExecutionDenied {
				t.Fatalf("denied dispatch = %+v", result)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("denied tool ran: marker error = %v", err)
			}
			waitForSessionStatus(t, s, contracts.SessionBusy)
			_, events, unsubscribe, err := s.JoinSessionEvents()
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribe()
			call.ID = "approved"
			done = dispatchSessionMCP(s, call)
			resolveSessionMCPApproval(t, s, contracts.ApprovalApprove)
			result = receiveSessionMCPDispatch(t, done)
			if result.canceled || len(result.messages) != 1 || result.messages[0].Status != contracts.ToolExecutionSuccess || result.messages[0].Content == nil || !strings.Contains(*result.messages[0].Content, "call=1") {
				t.Fatalf("approved dispatch = %+v", result)
			}
			for {
				event := receiveEvent(t, events)
				if event.Kind == contracts.EventToolResult && event.ToolResult.CallID == call.ID {
					if event.ToolResult.Status != contracts.ToolExecutionSuccess || !strings.Contains(event.ToolResult.Output, "call=1") {
						t.Fatalf("MCP execution event = %+v", event.ToolResult)
					}
					break
				}
			}
		})
	}
}

func TestSessionMCPCancellationDeniesQueuedCalls(t *testing.T) {
	configureSessionMCPTest(t)
	s := newMCPRuntimeTestSession(t, contracts.InteractiveAgent)
	marker := filepath.Join(t.TempDir(), "started")
	queuedMarker := filepath.Join(t.TempDir(), "queued")
	first := sessionMCPTestCall(t, s, "running", "block", marker)
	second := sessionMCPTestCall(t, s, "queued", "", queuedMarker)
	done := dispatchSessionMCP(s, first, second)
	resolveSessionMCPApproval(t, s, contracts.ApprovalApprove)
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("MCP execution never started")
		case <-time.After(time.Millisecond):
		}
	}
	waitForSessionStatus(t, s, contracts.SessionBusy)
	if err := s.CancelTurn(); err != nil {
		t.Fatal(err)
	}
	result := receiveSessionMCPDispatch(t, done)
	if !result.canceled || len(result.messages) != 2 {
		t.Fatalf("canceled dispatch = %+v", result)
	}
	for _, message := range result.messages {
		if message.Status != contracts.ToolExecutionDenied || message.Content == nil || !strings.Contains(*message.Content, "turn_canceled") {
			t.Fatalf("canceled tool message = %+v", message)
		}
	}
	if _, err := os.Stat(queuedMarker); !os.IsNotExist(err) {
		t.Fatalf("queued MCP tool ran: marker error = %v", err)
	}
}

func TestSessionMCPExecutionError(t *testing.T) {
	configureSessionMCPTest(t)
	s := newMCPRuntimeTestSession(t, contracts.InteractiveAgent)
	_, events, unsubscribe, err := s.JoinSessionEvents()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	call := sessionMCPTestCall(t, s, "error", "error", "")
	done := dispatchSessionMCP(s, call)
	resolveSessionMCPApproval(t, s, contracts.ApprovalApprove)
	result := receiveSessionMCPDispatch(t, done)
	if result.canceled || len(result.messages) != 1 || result.messages[0].Status != contracts.ToolExecutionError || result.messages[0].Content == nil || !strings.Contains(*result.messages[0].Content, "mcp_tool_error") {
		t.Fatalf("failed MCP dispatch = %+v", result)
	}
	for {
		event := receiveEvent(t, events)
		if event.Kind == contracts.EventToolResult && event.ToolResult.CallID == call.ID {
			if event.ToolResult.Status != contracts.ToolExecutionError || event.ToolResult.Error == "" || !strings.Contains(event.ToolResult.Output, "probe failed") {
				t.Fatalf("MCP error event = %+v", event.ToolResult)
			}
			break
		}
	}
}

func TestSessionMCPIndependentManagers(t *testing.T) {
	configureSessionMCPTest(t)
	first := newMCPRuntimeTestSession(t, contracts.InteractiveAgent)
	second := newMCPRuntimeTestSession(t, contracts.InteractiveAgent)
	if first.mcpManager == second.mcpManager {
		t.Fatal("sessions share MCP manager")
	}
	first.Close()
	call := sessionMCPTestCall(t, second, "independent", "", "")
	done := dispatchSessionMCP(second, call)
	resolveSessionMCPApproval(t, second, contracts.ApprovalApprove)
	result := receiveSessionMCPDispatch(t, done)
	if result.canceled || len(result.messages) != 1 || result.messages[0].Status != contracts.ToolExecutionSuccess || result.messages[0].Content == nil || !strings.Contains(*result.messages[0].Content, "call=1") {
		t.Fatalf("independent session dispatch = %+v", result)
	}
}
