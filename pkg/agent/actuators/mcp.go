package actuators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dipankardas011/infai/internal/config"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPTimeout = 60 * time.Second
	// mcpClientName is the implementation name the harness reports to MCP servers.
	mcpClientName = "infaiw"
)

type mcpToolBinding struct {
	session *mcp.ClientSession
	name    string
	timeout time.Duration
}

type mcpPromptBinding struct {
	session *mcp.ClientSession
	name    string
	timeout time.Duration
}

type mcpResourceBinding struct {
	session *mcp.ClientSession
	timeout time.Duration
}

// ElicitFunc lets a server ask the user a question mid-call. It blocks until
// the answer arrives; returning an error aborts the call.
type ElicitFunc func(ctx context.Context, server string, params *mcp.ElicitParams) (*mcp.ElicitResult, error)

// MCPManager owns a session's connections and its initial tool discovery snapshot.
type MCPManager struct {
	ctx              context.Context
	cancel           context.CancelFunc
	tools            []contracts.Tool
	bindings         map[contracts.ToolType]mcpToolBinding
	prompts          []contracts.MCPPrompt
	promptBindings   map[string]mcpPromptBinding
	resources        []contracts.MCPResource
	resourceBindings map[string]mcpResourceBinding
	sessions         []*mcp.ClientSession
	http             []*http.Transport
	closeOnce        sync.Once
	closeErr         error
}

func NewMCPManager(ctx context.Context, cwd string, logger *slog.Logger, servers map[string]contracts.MCPServerConfig, elicit ElicitFunc) (*MCPManager, error) {
	ctx, cancel := context.WithCancel(ctx)
	manager := &MCPManager{
		ctx:              ctx,
		cancel:           cancel,
		bindings:         make(map[contracts.ToolType]mcpToolBinding),
		promptBindings:   make(map[string]mcpPromptBinding),
		resourceBindings: make(map[string]mcpResourceBinding),
	}
	// On failure the caller gets nil, so nothing downstream can ever close what
	// the loop already connected. Close is also the only thing that stops a
	// stdio server: the SDK ignores ctx cancellation for client connections.
	// § go-sdk cmd.go pipeRWC.Close, internal/jsonrpc2 notDone
	allServersReady := false
	defer func() {
		if !allServersReady {
			_ = manager.Close()
		}
	}()

	for name := range servers {
		server := servers[name]
		timeout := defaultMCPTimeout
		if server.TimeoutSeconds > 0 {
			timeout = time.Duration(server.TimeoutSeconds) * time.Second
		}
		// One client per server: the elicitation handler is installed on the
		// client and has to report which server asked. § go-sdk client.go
		// ClientOptions.ElicitationHandler / mrtr.go multi-round-trip middleware
		serverName := name
		client := mcp.NewClient(&mcp.Implementation{Name: mcpClientName, Version: config.Version()}, &mcp.ClientOptions{
			Logger:       logger,
			Capabilities: &mcp.ClientCapabilities{},
			ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				if elicit == nil {
					return nil, errors.New("elicitation is not configured")
				}
				return elicit(ctx, serverName, req.Params)
			},
		})
		transport, err := manager.transport(cwd, server, timeout)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		startupCtx, stop := context.WithTimeout(ctx, timeout)
		session, err := client.Connect(startupCtx, transport, nil)
		if err != nil {
			stop()
			return nil, fmt.Errorf("MCP server %q: connection failed", name)
		}
		manager.sessions = append(manager.sessions, session)
		err = manager.discover(startupCtx, session, name, timeout)
		if err == nil {
			err = manager.discoverPrompts(startupCtx, session, name, timeout)
		}
		if err == nil {
			err = manager.discoverResources(startupCtx, session, name, timeout)
		}
		stop()
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allServersReady = true
	return manager, nil
}

func (m *MCPManager) transport(cwd string, server contracts.MCPServerConfig, timeout time.Duration) (mcp.Transport, error) {
	switch server.Transport {
	case "stdio":
		command := exec.Command(server.Command, server.Args...)
		command.Dir = cwd
		command.Env = os.Environ()
		command.Stderr = io.Discard
		command.WaitDelay = time.Second
		for key, value := range server.Env {
			command.Env = append(command.Env, key+"="+value)
		}
		return &mcp.CommandTransport{Command: command}, nil
	case "streamable-http":
		headers := make(http.Header)
		for key, value := range server.Headers {
			headers.Set(key, value)
		}
		if server.BearerTokenEnv != "" {
			token, ok := os.LookupEnv(server.BearerTokenEnv)
			if !ok || token == "" {
				return nil, errors.New("a referenced environment credential is missing")
			}
			if strings.ContainsAny(token, "\r\n") {
				return nil, errors.New("the referenced bearer credential is invalid")
			}
			headers.Set("Authorization", "Bearer "+token)
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		m.http = append(m.http, base)
		return &mcp.StreamableClientTransport{
			Endpoint: server.URL,
			HTTPClient: &http.Client{
				Timeout:   timeout,
				Transport: &mcpHeaderTransport{base: base, headers: headers},
				// Credentials must stay on the configured endpoint. § MCP authorization: token audience binding.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
			MaxEventSize:         webMaxBodyBytes,
		}, nil
	default:
		return nil, errors.New("unsupported transport")
	}
}

type mcpHeaderTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *mcpHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	for key, values := range t.headers {
		request.Header[key] = slices.Clone(values)
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(response.Body, webMaxBodyBytes+1), response.Body}
	return response, nil
}

func (m *MCPManager) discover(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	cursor := ""
	seen := make(map[string]bool)
	for {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return errors.New("tool discovery failed")
		}
		for _, tool := range page.Tools {
			if tool == nil || tool.Name == "" {
				return errors.New("server returned an invalid tool")
			}
			name := mcpToolName(server, tool.Name)
			if _, exists := m.bindings[contracts.ToolType(name)]; exists {
				return errors.New("server returned duplicate tool names")
			}
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return fmt.Errorf("server returned an invalid input schema, err: %w", err)
			}
			m.tools = append(m.tools, contracts.Tool{Name: name, Description: tool.Description, Parameters: schema})
			m.bindings[contracts.ToolType(name)] = mcpToolBinding{session: session, name: tool.Name, timeout: timeout}
		}
		if page.NextCursor == "" {
			return nil
		}
		if seen[page.NextCursor] {
			return errors.New("server returned a repeated pagination cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func mcpToolName(server, tool string) string {
	return contracts.MCPToolPrefix + server + "_" + tool
}

func (m *MCPManager) discoverPrompts(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	cursor := ""
	seen := make(map[string]bool)
	for {
		page, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return errors.New("prompt discovery failed")
		}
		for _, prompt := range page.Prompts {
			if prompt == nil || prompt.Name == "" {
				return errors.New("server returned an invalid prompt")
			}
			key := server + ":" + prompt.Name
			if _, exists := m.promptBindings[key]; exists {
				return errors.New("server returned duplicate prompt names")
			}
			arguments := make([]contracts.MCPPromptArgument, 0, len(prompt.Arguments))
			for _, argument := range prompt.Arguments {
				if argument == nil {
					continue
				}
				arguments = append(arguments, contracts.MCPPromptArgument{Name: argument.Name, Description: argument.Description, Required: argument.Required})
			}
			m.prompts = append(m.prompts, contracts.MCPPrompt{Server: server, Name: prompt.Name, Title: prompt.Title, Description: prompt.Description, Arguments: arguments})
			m.promptBindings[key] = mcpPromptBinding{session: session, name: prompt.Name, timeout: timeout}
		}
		if page.NextCursor == "" {
			return nil
		}
		if seen[page.NextCursor] {
			return errors.New("server returned a repeated pagination cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// discoverResources mirrors discoverPrompts, but a server that does not
// advertise the resource capability is skipped so its absence cannot fail the
// session.
func (m *MCPManager) discoverResources(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	if result := session.InitializeResult(); result != nil && result.Capabilities.Resources == nil {
		return nil
	}
	cursor := ""
	seen := make(map[string]bool)
	// Both the duplicate guard and the binding map are server-qualified, so two
	// servers advertising the same URI cannot collide.
	seenURIs := make(map[string]bool)
	for {
		page, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return errors.New("resource discovery failed")
		}
		for _, resource := range page.Resources {
			if resource == nil || resource.URI == "" {
				return errors.New("server returned an invalid resource")
			}
			key := server + ":" + resource.URI
			if seenURIs[key] {
				return errors.New("server returned duplicate resource URIs")
			}
			seenURIs[key] = true
			m.resources = append(m.resources, contracts.MCPResource{Server: server, URI: resource.URI, Name: resource.Name, Title: resource.Title, Description: resource.Description, MIMEType: resource.MIMEType, Size: resource.Size})
			m.resourceBindings[server+"|"+resource.URI] = mcpResourceBinding{session: session, timeout: timeout}
		}
		if page.NextCursor == "" {
			return nil
		}
		if seen[page.NextCursor] {
			return errors.New("server returned a repeated pagination cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

func (m *MCPManager) Tools() []contracts.Tool { return slices.Clone(m.tools) }

func (m *MCPManager) Prompts() []contracts.MCPPrompt { return slices.Clone(m.prompts) }

func (m *MCPManager) Resources() []contracts.MCPResource { return slices.Clone(m.resources) }

// HasResource reports whether this session's catalogue can read a URI. The
// catalogue is small and fixed after discovery, so a scan costs less than a
// second index over the same data.
func (m *MCPManager) HasResource(uri string) bool {
	for _, resource := range m.resources {
		if resource.URI == uri {
			return true
		}
	}
	return false
}

func (m *MCPManager) ReadResource(ctx context.Context, uri string) (contracts.MCPResourceRead, error) {
	server, binding, ok := m.resourceBinding(uri)
	if !ok {
		return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "unknown_resource", "the MCP resource is not registered", contracts.ResponsibilityAgent, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, binding.timeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	if m.ctx.Err() != nil {
		return contracts.MCPResourceRead{}, m.ctx.Err()
	}
	result, err := binding.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		if ctx.Err() != nil {
			return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "execution_canceled", "the MCP resource read was canceled or exceeded its deadline", contracts.ResponsibilityEnvironment, ctx.Err())
		}
		return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "mcp_resource_failed", "the MCP server could not read the resource", contracts.ResponsibilityEnvironment, nil)
	}
	if result.NeedsInput() {
		return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "unsupported_input", "the MCP server requested an unsupported client capability", contracts.ResponsibilityEnvironment, nil)
	}
	parts := make([]string, 0, len(result.Contents))
	mimeType := ""
	for _, item := range result.Contents {
		switch {
		case item == nil:
			return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "unsupported_content", "the MCP resource is not text", contracts.ResponsibilityEnvironment, nil)
		// Blob holds the decoded bytes; the JSON layer handled the base64.
		case len(item.Blob) > 0:
			if !textResourceMIME(item.MIMEType) {
				return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "unsupported_content", "the MCP resource is not text", contracts.ResponsibilityEnvironment, nil)
			}
			parts = append(parts, string(item.Blob))
		default:
			parts = append(parts, item.Text)
		}
		if mimeType == "" {
			mimeType = item.MIMEType
		}
	}
	return contracts.MCPResourceRead{Server: server, URI: uri, MIMEType: mimeType, Text: clipWebContent(strings.Join(parts, "\n"), webMaxBodyBytes)}, nil
}

// resourceBinding resolves a URI against the catalogue snapshot: an MCP
// session belongs to one server, so the URI alone cannot say which
// connection to read through.
func (m *MCPManager) resourceBinding(uri string) (string, mcpResourceBinding, bool) {
	for _, resource := range m.resources {
		if resource.URI != uri {
			continue
		}
		binding, ok := m.resourceBindings[resource.Server+"|"+uri]
		if !ok {
			return "", mcpResourceBinding{}, false
		}
		return resource.Server, binding, true
	}
	return "", mcpResourceBinding{}, false
}

// textResourceMIME reports whether a resource's declared MIME type can be
// surfaced to the model as text.
func textResourceMIME(mimeType string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/xml", "application/yaml":
		return true
	}
	return strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") || strings.HasSuffix(mimeType, "+yaml")
}

func (m *MCPManager) HasTool(name contracts.ToolType) bool {
	_, ok := m.bindings[name]
	return ok
}

func (m *MCPManager) Execute(ctx context.Context, call contracts.ToolCall) (string, error) {
	binding, ok := m.bindings[call.Function.Name]
	if !ok {
		return "", contracts.NewToolExecutionError(call.Function.Name, "unknown_tool", "the MCP tool is not registered", contracts.ResponsibilityAgent, nil)
	}
	if _, err := contracts.DecodeToolArguments[map[string]json.RawMessage](call.Function.Name, call); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, binding.timeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	if m.ctx.Err() != nil {
		return "", m.ctx.Err()
	}
	result, err := binding.session.CallTool(ctx, &mcp.CallToolParams{Name: binding.name, Arguments: json.RawMessage(call.Function.Arguments)})
	if err != nil {
		if ctx.Err() != nil {
			return "", contracts.NewToolExecutionError(call.Function.Name, "execution_canceled", "the MCP call was canceled or exceeded its deadline", contracts.ResponsibilityEnvironment, ctx.Err())
		}
		return "", contracts.NewToolExecutionError(call.Function.Name, "mcp_call_failed", "the MCP server could not complete the call", contracts.ResponsibilityEnvironment, nil)
	}
	if result.NeedsInput() {
		return "", contracts.NewToolExecutionError(call.Function.Name, "unsupported_input", "the MCP server requested an unsupported client capability", contracts.ResponsibilityEnvironment, nil)
	}
	for _, block := range result.Content {
		if _, ok := block.(*mcp.TextContent); !ok {
			return "", contracts.NewToolExecutionError(call.Function.Name, "unsupported_content", "the MCP call completed but returned content other than text/JSON", contracts.ResponsibilityEnvironment, nil)
		}
	}
	output, err := json.Marshal(struct {
		Content           []mcp.Content `json:"content"`
		StructuredContent any           `json:"structuredContent,omitempty"`
		IsError           bool          `json:"isError"`
	}{result.Content, result.StructuredContent, result.IsError})
	if err != nil {
		return "", contracts.NewToolExecutionError(call.Function.Name, "invalid_result", "the MCP result could not be encoded", contracts.ResponsibilityEnvironment, nil)
	}
	text := clipWebContent(string(output), maxToolContentBytes)
	if result.IsError {
		return text, contracts.NewToolExecutionError(call.Function.Name, "mcp_tool_error", text, contracts.ResponsibilityEnvironment, nil)
	}
	return text, nil
}

func (m *MCPManager) RenderPrompt(ctx context.Context, server, name string, arguments map[string]string) ([]contracts.ChatMessage, error) {
	callName := contracts.ToolType(server + ":" + name)
	binding, ok := m.promptBindings[server+":"+name]
	if !ok {
		return nil, contracts.NewToolExecutionError(callName, "unknown_prompt", "the MCP prompt is not registered", contracts.ResponsibilityAgent, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, binding.timeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	if m.ctx.Err() != nil {
		return nil, m.ctx.Err()
	}

	result, err := binding.session.GetPrompt(ctx, &mcp.GetPromptParams{Name: binding.name, Arguments: arguments})
	if err != nil {
		if ctx.Err() != nil {
			return nil, contracts.NewToolExecutionError(callName, "execution_canceled", "the MCP prompt was canceled or exceeded its deadline", contracts.ResponsibilityEnvironment, ctx.Err())
		}
		return nil, contracts.NewToolExecutionError(callName, "mcp_prompt_failed", "the MCP server could not render the prompt", contracts.ResponsibilityEnvironment, nil)
	}

	messages := make([]contracts.ChatMessage, 0, len(result.Messages))
	for _, message := range result.Messages {
		if message == nil {
			return nil, contracts.NewToolExecutionError(callName, "unsupported_content", "the MCP prompt returned content other than text", contracts.ResponsibilityEnvironment, nil)
		}

		content, ok := message.Content.(*mcp.TextContent)
		if !ok {
			return nil, contracts.NewToolExecutionError(callName, "unsupported_content", "the MCP prompt returned content other than text", contracts.ResponsibilityEnvironment, nil)
		}

		switch message.Role {
		case mcp.Role("user"):
			messages = append(messages, contracts.NewUserMessage(content.Text))
		case mcp.Role("assistant"):
			messages = append(messages, contracts.NewAssistantMessage(content.Text))
		default:
			return nil, contracts.NewToolExecutionError(callName, "unsupported_content", "the MCP prompt returned a message with an unsupported role", contracts.ResponsibilityEnvironment, nil)
		}
	}
	return messages, nil
}

func (m *MCPManager) Close() error {
	m.closeOnce.Do(func() {
		m.cancel()
		for _, session := range m.sessions {
			if err := session.Close(); err != nil {
				m.closeErr = errors.New("MCP connection shutdown failed")
			}
		}
		for _, transport := range m.http {
			transport.CloseIdleConnections()
		}
	})
	return m.closeErr
}
