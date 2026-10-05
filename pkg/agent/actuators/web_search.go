package actuators

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// exaMCPEndpoint is the keyless Exa MCP server. It is fixed: the model supplies
// only a query string, never a URL, so the search path has no SSRF surface.
// It is a var so tests can point it at a local server.
var exaMCPEndpoint = "https://mcp.exa.ai/mcp"

const (
	exaMCPTimeout        = 30 * time.Second
	exaMCPMaxBody        = 1 << 20
	exaMCPProtocol       = "2024-11-05"
	exaMCPClientName     = "infai"
	exaMCPClientVersion  = "0.0.1"
	exaMCPDefaultResults = 5
	exaMCPMaxResults     = 10
	exaMCPSearchToolName = "web_search_exa"
	exaMCPSessionHeader  = "Mcp-Session-Id"
)

func WebSearchTool() contracts.Tool {
	return toolSchema(
		"websearch",
		"Search the web and return result titles, URLs and content snippets. Use for current information or to find pages. To read one page in full, use webfetch instead.",
		map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural-language description of the page you want to find",
			},
			"objective": map[string]any{
				"type":        "string",
				"description": "Which documents should rank first and what facts to pull from them; defaults to the query",
			},
			"num_results": map[string]any{
				"type":        "integer",
				"description": "Number of results, 1 to 10; defaults to 5",
			},
		},
		[]string{"query"},
	)
}

type webSearchArguments struct {
	Query      string `json:"query"`
	Objective  string `json:"objective"`
	NumResults *int   `json:"num_results,omitempty"`
}

func (m *FileManager) WebSearchExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[webSearchArguments](contracts.WebSearchTool, tc)
	if err != nil {
		return "", err
	}
	if err := webSearchValidate(args); err != nil {
		return "", wrapToolError(contracts.WebSearchTool, err, "invalid_arguments", "websearch arguments are invalid")
	}

	return contracts.RunBounded(ctx, contracts.WebSearchTool, exaMCPTimeout+5*time.Second, func() (string, error) {
		output, err := exaSearch(ctx, args)
		if err != nil {
			return "", wrapToolError(contracts.WebSearchTool, err, "search_failed", "the search could not be completed")
		}
		return output, nil
	})
}

func webSearchValidate(args webSearchArguments) error {
	if strings.TrimSpace(args.Query) == "" {
		return contracts.NewToolExecutionError(contracts.WebSearchTool, "invalid_arguments", "websearch requires a query", contracts.ResponsibilityAgent, nil)
	}
	if err := validateText(args.Query, contracts.ResponsibilityAgent); err != nil {
		return err
	}
	if args.NumResults != nil && (*args.NumResults < 1 || *args.NumResults > exaMCPMaxResults) {
		return contracts.NewToolExecutionError(contracts.WebSearchTool, "invalid_arguments", "num_results must be between 1 and 10", contracts.ResponsibilityAgent, nil)
	}
	return nil
}

// exaSearch performs the MCP handshake (initialize -> initialized -> tools/call)
// and returns the markdown text the server produced.
func exaSearch(ctx context.Context, args webSearchArguments) (string, error) {
	client := &http.Client{Timeout: exaMCPTimeout}

	_, headers, err := exaMCPRequest(ctx, client, "", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": exaMCPProtocol,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": exaMCPClientName, "version": exaMCPClientVersion},
		},
	})
	if err != nil {
		return "", filesystemErr("search_failed", "the search service could not be reached", contracts.ResponsibilityEnvironment, err)
	}
	session := headers.Get(exaMCPSessionHeader)

	// notifications/initialized carries no id and expects no result; a failure
	// here is not fatal, since tools/call below is the real test.
	_, _, _ = exaMCPRequest(ctx, client, session, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	objective := strings.TrimSpace(args.Objective)
	if objective == "" {
		objective = strings.TrimSpace(args.Query)
	}
	count := exaMCPDefaultResults
	if args.NumResults != nil {
		count = *args.NumResults
	}

	result, _, err := exaMCPRequest(ctx, client, session, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": exaMCPSearchToolName,
			"arguments": map[string]any{
				"query":      args.Query,
				"objective":  objective,
				"numResults": count,
			},
		},
	})
	if err != nil {
		return "", filesystemErr("search_failed", "the search service returned an error", contracts.ResponsibilityEnvironment, err)
	}

	text, err := exaMCPText(result)
	if err != nil {
		return "", filesystemErr("search_failed", "the search returned no usable result", contracts.ResponsibilityEnvironment, err)
	}
	return clipWebContent(strings.TrimSpace(text), maxToolContentBytes), nil
}

// exaMCPRequest sends one JSON-RPC request and returns its `result` member plus
// the response headers (the session id arrives in a header).
func exaMCPRequest(ctx context.Context, client *http.Client, session string, payload map[string]any) (json.RawMessage, http.Header, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, exaMCPEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		request.Header.Set(exaMCPSessionHeader, session)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()

	raw, _, err := readCapped(response.Body, exaMCPMaxBody)
	if err != nil {
		return nil, response.Header, err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return nil, response.Header, fmt.Errorf("search service responded %s", response.Status)
	}
	if response.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(raw)) == 0 {
		return nil, response.Header, nil
	}

	envelope, err := decodeMCPEnvelope(raw)
	if err != nil {
		return nil, response.Header, err
	}
	if envelope.Error != nil {
		return nil, response.Header, fmt.Errorf("search service: %s", envelope.Error.Message)
	}
	return envelope.Result, response.Header, nil
}

type mcpEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// decodeMCPEnvelope accepts both a plain JSON body and the SSE framing the
// server may use instead.
func decodeMCPEnvelope(raw []byte) (mcpEnvelope, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var envelope mcpEnvelope
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return mcpEnvelope{}, err
		}
		return envelope, nil
	}

	var data strings.Builder
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimSpace(after))
		}
	}
	if data.Len() == 0 {
		return mcpEnvelope{}, errors.New("empty response")
	}
	var envelope mcpEnvelope
	if err := json.Unmarshal([]byte(data.String()), &envelope); err != nil {
		return mcpEnvelope{}, err
	}
	return envelope, nil
}

// exaMCPText joins the text blocks of a tools/call result.
func exaMCPText(result json.RawMessage) (string, error) {
	if len(result) == 0 {
		return "", errors.New("empty result")
	}
	var call struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &call); err != nil {
		return "", err
	}
	if call.IsError {
		return "", errors.New("tool reported an error")
	}
	parts := make([]string, 0, len(call.Content))
	for _, block := range call.Content {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("no text content")
	}
	return strings.Join(parts, "\n"), nil
}
