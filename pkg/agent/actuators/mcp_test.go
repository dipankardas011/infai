package actuators

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStdioSmoke(t *testing.T) {
	if os.Getenv("INFAI_MCP_SMOKE_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "smoke", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)},
		func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv("SMOKE_TOKEN")}}}, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPConnectsAndCallsTool(t *testing.T) {
	manager, err := NewMCPManager(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler), map[string]contracts.MCPServerConfig{
		"smoke": {
			Transport: "stdio",
			Command:   os.Args[0],
			Args:      []string{"-test.run=^TestMCPStdioSmoke$"},
			Env:       map[string]string{"INFAI_MCP_SMOKE_HELPER": "1", "SMOKE_TOKEN": "connected"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	tools := manager.Tools()
	if len(tools) != 1 || !manager.HasTool(contracts.ToolType(tools[0].Name)) {
		t.Fatalf("tools = %+v", tools)
	}
	call := contracts.ToolCall{ID: "1", Function: contracts.Function{Name: contracts.ToolType(tools[0].Name), Arguments: `{}`}}
	output, err := manager.Execute(context.Background(), call)
	if err != nil || !strings.Contains(output, "connected") {
		t.Fatalf("output = %q, err = %v", output, err)
	}
}
