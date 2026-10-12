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
	"golang.org/x/oauth2"
)

const (
	defaultMCPTimeout = 60 * time.Second

	// mcpToolNameSeparator joins a server name and a tool name into the name the
	// model sees. A server name may itself contain it (§ the store allows
	// letters, digits, underscore and hyphen), so two servers can still produce
	// the same tool name: that is caught as a duplicate and the server that
	// collided is left out of the session.
	mcpToolNameSeparator = "_"
	// mcpPromptKeySeparator and mcpResourceKeySeparator build this session's own
	// lookup keys, not a name a model or a server ever sees. Neither character
	// can appear in a server name, so one key names exactly one server's entry —
	// which is why they are not the tool-name separator above.
	mcpPromptKeySeparator   = ":"
	mcpResourceKeySeparator = "|"

	// mcpMaxEventBytes bounds one server-sent event of an MCP connection.
	mcpMaxEventBytes = 2 << 20
	// mcpClientName is the implementation name the harness reports to MCP servers.
	mcpClientName = "infai"
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

// mcpUnauthorizedMessage is what a session reports when the server refuses the
// credential it was given: the same words whichever kind of call it happened on.
const mcpUnauthorizedMessage = "the MCP server rejected the client as unauthorized; its credentials are missing, expired or insufficient and must be refreshed"

// mcpCallError is one kind of MCP call, so that every way it can fail is
// reported the same way: a deadline, a refused credential, or anything else.
type mcpCallError struct {
	name     contracts.ToolType
	canceled string
	code     string
	failed   string
}

func (c mcpCallError) from(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		responsibility := contracts.ResponsibilityEnvironment
		if errors.Is(canceled, context.Canceled) {
			responsibility = contracts.ResponsibilitySession
		}
		return contracts.NewToolExecutionError(c.name, "execution_canceled", c.canceled, responsibility, canceled)
	}
	if errors.Is(err, errAuthRequired) {
		return contracts.NewToolExecutionError(c.name, "mcp_unauthorized", mcpUnauthorizedMessage, contracts.ResponsibilityUser, err)
	}
	return contracts.NewToolExecutionError(c.name, c.code, c.failed, contracts.ResponsibilityEnvironment, nil)
}

// begin bounds one MCP call by the server's own deadline and by the session's
// lifetime: the SDK does not watch the manager's context for these calls.
func (m *MCPManager) begin(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// ElicitFunc lets a server ask the user a question mid-call. It blocks until
// the answer arrives; returning an error aborts the call.
type ElicitFunc func(ctx context.Context, server string, params *mcp.ElicitParams) (*mcp.ElicitResult, error)

// MCPManager owns a session's connections and its initial tool discovery snapshot.
type MCPManager struct {
	ctx              context.Context
	cancel           context.CancelFunc
	logger           *slog.Logger
	tools            []contracts.Tool
	bindings         map[contracts.ToolType]mcpToolBinding
	prompts          []contracts.MCPPrompt
	promptBindings   map[string]mcpPromptBinding
	resources        []contracts.MCPResource
	resourceBindings map[string]mcpResourceBinding
	sessions         []*mcp.ClientSession
	http             []*http.Transport
	// unavailable is why a configured server is not part of this session. The
	// counts of what it offers cannot say it: a server that never connected and
	// one that offered nothing both offer nothing.
	unavailable map[string]string
	// configured is the server names this manager was given, in the order it was
	// given them: it is what a status report walks.
	configured []string
	closeOnce  sync.Once
	closeErr   error
}

func NewMCPManager(ctx context.Context, cwd string, logger *slog.Logger, servers map[string]contracts.MCPServerConfig, elicit ElicitFunc) (*MCPManager, error) {
	ctx, cancel := context.WithCancel(ctx)
	manager := &MCPManager{
		ctx:              ctx,
		cancel:           cancel,
		logger:           logger,
		bindings:         make(map[contracts.ToolType]mcpToolBinding),
		promptBindings:   make(map[string]mcpPromptBinding),
		resourceBindings: make(map[string]mcpResourceBinding),
		unavailable:      make(map[string]string),
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

	for name, server := range servers {
		manager.configured = append(manager.configured, name)
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
			manager.markUnavailable(name, err)
			continue
		}
		startupCtx, stop := context.WithTimeout(ctx, timeout)
		session, err := client.Connect(startupCtx, transport, nil)
		if err != nil {
			stop()
			manager.markUnavailable(name, err)
			continue
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
			manager.markUnavailable(name, err)
			continue
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allServersReady = true
	return manager, nil
}

func (m *MCPManager) markUnavailable(server string, err error) {
	reason := "connection or discovery failed"
	if errors.Is(err, errAuthRequired) {
		reason = errAuthRequired.Error()
	}
	m.unavailable[server] = reason
	m.logger.WarnContext(m.ctx, "MCP server unavailable", "server", server, "reason", reason)
}

func (m *MCPManager) Status() []contracts.MCPServerStatus {
	status := make([]contracts.MCPServerStatus, 0, len(m.unavailable))
	for _, server := range m.configured {
		if reason, ok := m.unavailable[server]; ok {
			status = append(status, contracts.MCPServerStatus{Server: server, Reason: reason})
		}
	}
	return status
}

func (m *MCPManager) Unavailable(server string) (string, bool) {
	reason, ok := m.unavailable[server]
	return reason, ok
}

func (m *MCPManager) transport(cwd string, server contracts.MCPServerConfig, timeout time.Duration) (mcp.Transport, error) {
	switch server.Transport {
	case contracts.MCPTransportStdio:
		command := exec.Command(server.Command, server.Args...)
		command.Dir = cwd
		command.Env = os.Environ()
		command.Stderr = io.Discard
		command.WaitDelay = time.Second
		for key, value := range server.Env {
			command.Env = append(command.Env, key+"="+value)
		}
		return &mcp.CommandTransport{Command: command}, nil
	case contracts.MCPTransportStreamableHTTP:
		headers := make(http.Header)
		for key, value := range server.Headers {
			headers.Set(key, value)
		}
		if server.BearerToken != "" {
			headers.Set("Authorization", "Bearer "+server.BearerToken)
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		m.http = append(m.http, base)
		transport := &mcp.StreamableClientTransport{
			Endpoint: server.URL,
			HTTPClient: &http.Client{
				Timeout:   timeout,
				Transport: &mcpHeaderTransport{base: base, headers: headers},
				// Credentials must stay on the configured endpoint. § MCP authorization: token audience binding.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
			MaxEventSize:         mcpMaxEventBytes,
		}

		handler := &mcpSessionAuth{}
		if oauth := server.OAuth; oauth != nil {
			handler.token = &oauth2.Token{AccessToken: oauth.AccessToken, Expiry: oauth.Expiry}
		}
		transport.OAuthHandler = handler
		return transport, nil
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

// walkPages reads one paginated listing to its end. A cursor that repeats would
// loop forever, so it is a failure. Every listing the manager reads goes through
// here: three hand-written copies had to be kept in step by hand.
func walkPages[T any](next func(cursor string) (items []T, nextCursor string, err error), visit func(T) error) error {
	cursor := ""
	seen := make(map[string]bool)
	for {
		items, nextCursor, err := next(cursor)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := visit(item); err != nil {
				return err
			}
		}
		if nextCursor == "" {
			return nil
		}
		if seen[nextCursor] {
			return errors.New("server returned a repeated pagination cursor")
		}
		seen[nextCursor] = true
		cursor = nextCursor
	}
}

func (m *MCPManager) discover(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	return walkPages(func(cursor string) ([]*mcp.Tool, string, error) {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, "", fmt.Errorf("tool discovery failed: %w", err)
		}
		return page.Tools, page.NextCursor, nil
	}, func(tool *mcp.Tool) error {
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
		return nil
	})
}

func mcpToolName(server, tool string) string {
	return contracts.MCPToolPrefix + server + mcpToolNameSeparator + tool
}
func mcpPromptKey(server, name string) string  { return server + mcpPromptKeySeparator + name }
func mcpResourceKey(server, uri string) string { return server + mcpResourceKeySeparator + uri }

func (m *MCPManager) discoverPrompts(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	return walkPages(func(cursor string) ([]*mcp.Prompt, string, error) {
		page, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, "", fmt.Errorf("prompt discovery failed: %w", err)
		}
		return page.Prompts, page.NextCursor, nil
	}, func(prompt *mcp.Prompt) error {
		if prompt == nil || prompt.Name == "" {
			return errors.New("server returned an invalid prompt")
		}
		key := mcpPromptKey(server, prompt.Name)
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
		return nil
	})
}

// discoverResources mirrors discoverPrompts, but a server that does not
// advertise the resource capability is skipped so its absence cannot fail the
// session.
func (m *MCPManager) discoverResources(ctx context.Context, session *mcp.ClientSession, server string, timeout time.Duration) error {
	if result := session.InitializeResult(); result != nil && result.Capabilities.Resources == nil {
		return nil
	}
	// Both the duplicate guard and the binding map are server-qualified, so two
	// servers advertising the same URI cannot collide.
	seenURIs := make(map[string]bool)
	return walkPages(func(cursor string) ([]*mcp.Resource, string, error) {
		page, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, "", fmt.Errorf("resource discovery failed: %w", err)
		}
		return page.Resources, page.NextCursor, nil
	}, func(resource *mcp.Resource) error {
		if resource == nil || resource.URI == "" {
			return errors.New("server returned an invalid resource")
		}
		key := mcpResourceKey(server, resource.URI)
		if seenURIs[key] {
			return errors.New("server returned duplicate resource URIs")
		}
		seenURIs[key] = true
		m.resources = append(m.resources, contracts.MCPResource{Server: server, URI: resource.URI, Name: resource.Name, Title: resource.Title, Description: resource.Description, MIMEType: resource.MIMEType, Size: resource.Size})
		m.resourceBindings[key] = mcpResourceBinding{session: session, timeout: timeout}
		return nil
	})
}

func (m *MCPManager) Tools() []contracts.Tool { return slices.Clone(m.tools) }

func (m *MCPManager) Prompts() []contracts.MCPPrompt { return slices.Clone(m.prompts) }

func (m *MCPManager) Resources() []contracts.MCPResource { return slices.Clone(m.resources) }

// HasResource reports whether this session's catalogue can read a URI. The
// catalogue is small and fixed after discovery, so a scan costs less than a
// second index over the same data.
func (m *MCPManager) HasResource(uri string) bool {
	_, _, ok := m.resourceBinding(uri)
	return ok
}

func (m *MCPManager) ReadResource(ctx context.Context, uri string) (contracts.MCPResourceRead, error) {
	server, binding, ok := m.resourceBinding(uri)
	if !ok {
		return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "unknown_resource", "the MCP resource is not registered", contracts.ResponsibilityAgent, nil)
	}
	failure := mcpCallError{
		name:     contracts.ToolType(uri),
		canceled: "the MCP resource read was canceled or exceeded its deadline",
		code:     "mcp_resource_failed",
		failed:   "the MCP server could not read the resource",
	}
	ctx, cancel := m.begin(ctx, binding.timeout)
	defer cancel()
	if m.ctx.Err() != nil {
		return contracts.MCPResourceRead{}, m.ctx.Err()
	}
	result, err := binding.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return contracts.MCPResourceRead{}, failure.from(ctx, err)
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
	return contracts.MCPResourceRead{Server: server, URI: uri, MIMEType: mimeType, Text: clipWebContent(strings.Join(parts, "\n"), maxToolContentBytes)}, nil
}

// resourceBinding resolves a URI against the catalogue snapshot: an MCP
// session belongs to one server, so the URI alone cannot say which
// connection to read through.
func (m *MCPManager) resourceBinding(uri string) (string, mcpResourceBinding, bool) {
	for _, resource := range m.resources {
		if resource.URI != uri {
			continue
		}
		binding, ok := m.resourceBindings[mcpResourceKey(resource.Server, uri)]
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
	failure := mcpCallError{
		name:     call.Function.Name,
		canceled: "the MCP call was canceled or exceeded its deadline",
		code:     "mcp_call_failed",
		failed:   "the MCP server could not complete the call",
	}
	ctx, cancel := m.begin(ctx, binding.timeout)
	defer cancel()
	if m.ctx.Err() != nil {
		return "", m.ctx.Err()
	}
	result, err := binding.session.CallTool(ctx, &mcp.CallToolParams{Name: binding.name, Arguments: json.RawMessage(call.Function.Arguments)})
	if err != nil {
		return "", failure.from(ctx, err)
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
	callName := contracts.ToolType(mcpPromptKey(server, name))
	binding, ok := m.promptBindings[mcpPromptKey(server, name)]
	if !ok {
		return nil, contracts.NewToolExecutionError(callName, "unknown_prompt", "the MCP prompt is not registered", contracts.ResponsibilityAgent, nil)
	}
	failure := mcpCallError{
		name:     callName,
		canceled: "the MCP prompt was canceled or exceeded its deadline",
		code:     "mcp_prompt_failed",
		failed:   "the MCP server could not render the prompt",
	}
	ctx, cancel := m.begin(ctx, binding.timeout)
	defer cancel()
	if m.ctx.Err() != nil {
		return nil, m.ctx.Err()
	}

	result, err := binding.session.GetPrompt(ctx, &mcp.GetPromptParams{Name: binding.name, Arguments: arguments})
	if err != nil {
		return nil, failure.from(ctx, err)
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
