package actuators

import (
	"context"
	"encoding/json"
	"errors"
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

func TestMCPStdioResources(t *testing.T) {
	if os.Getenv("INFAI_MCP_RESOURCE_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "resources", Version: "1"}, nil)
	server.AddResource(&mcp.Resource{URI: "dummy://readme", Name: "readme", MIMEType: "text/plain", Size: 14},
		func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "text/plain", Text: "hello resource"}}}, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPConnectsAndReadsResource(t *testing.T) {
	manager, err := NewMCPManager(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler), map[string]contracts.MCPServerConfig{
		"resource": {
			Transport: "stdio",
			Command:   os.Args[0],
			Args:      []string{"-test.run=^TestMCPStdioResources$"},
			Env:       map[string]string{"INFAI_MCP_RESOURCE_HELPER": "1"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	resources := manager.Resources()
	if len(resources) != 1 {
		t.Fatalf("resources = %+v", resources)
	}
	if resource := resources[0]; resource.Server != "resource" || resource.URI != "dummy://readme" || resource.Name != "readme" || resource.MIMEType != "text/plain" || resource.Size != 14 {
		t.Fatalf("resource = %+v", resource)
	}

	read, err := manager.ReadResource(context.Background(), "dummy://readme")
	if err != nil || read.Text != "hello resource" || read.Server != "resource" || read.URI != "dummy://readme" || read.MIMEType != "text/plain" {
		t.Fatalf("read = %+v, err = %v", read, err)
	}

	if !manager.HasResource("dummy://readme") {
		t.Fatal("HasResource(offered URI) = false, want true")
	}
	for _, uri := range []string{"dummy://missing", "", "dummy://"} {
		if manager.HasResource(uri) {
			t.Fatalf("HasResource(%q) = true, want false", uri)
		}
	}

	if _, err := manager.ReadResource(context.Background(), "dummy://missing"); err == nil {
		t.Fatal("expected error for an unregistered resource")
	} else {
		var execution *contracts.ExecutionError
		if !errors.As(err, &execution) || execution.Code != "unknown_resource" {
			t.Fatalf("err = %v, want unknown_resource execution error", err)
		}
	}
}

func TestMCPConnectsWithoutResources(t *testing.T) {
	manager, err := NewMCPManager(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler), map[string]contracts.MCPServerConfig{
		"smoke": {
			Transport: "stdio",
			Command:   os.Args[0],
			Args:      []string{"-test.run=^TestMCPStdioSmoke$"},
			Env:       map[string]string{"INFAI_MCP_SMOKE_HELPER": "1"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if resources := manager.Resources(); len(resources) != 0 {
		t.Fatalf("resources = %+v", resources)
	}
}

func TestMCPConnectsAndCallsTool(t *testing.T) {
	manager, err := NewMCPManager(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler), map[string]contracts.MCPServerConfig{
		"smoke": {
			Transport: "stdio",
			Command:   os.Args[0],
			Args:      []string{"-test.run=^TestMCPStdioSmoke$"},
			Env:       map[string]string{"INFAI_MCP_SMOKE_HELPER": "1", "SMOKE_TOKEN": "connected"},
		},
	}, nil)
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
