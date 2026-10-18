package actuators

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// exaMCPEndpoint is the keyless Exa MCP server. It is fixed: the model supplies
// only a query string, never a URL, so the search path has no SSRF surface.
// It is a var so tests can point it at a local server.
var exaMCPEndpoint = "https://mcp.exa.ai/mcp"

// ddgEndpoint is the keyless DuckDuckGo Instant Answer API. Like exaMCPEndpoint
// it is fixed; only the query is model-controlled.
var ddgEndpoint = "https://api.duckduckgo.com/"

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

	// ddgTimeout bounds only the fallback attempt, so a slow primary search
	// still leaves time to try DuckDuckGo.
	ddgTimeout = 10 * time.Second
	ddgMaxBody = 1 << 20
	// DuckDuckGo's API terms require a descriptive t= parameter and attribution
	// in the response. § https://duckduckgo.com/api
	ddgAppName = "infai"
	// The default Go user agent is answered with 202 instead of 200.
	ddgUserAgent = "infai-websearch/1.0"
	// DuckDuckGo throttles high request rates. Pause before the fallback so a
	// burst of failed Exa calls does not turn into a burst against DDG.
	ddgRequestPause = 2 * time.Second
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

	return contracts.RunBounded(ctx, contracts.WebSearchTool, exaMCPTimeout+ddgTimeout+5*time.Second, func() (string, error) {
		output, err := exaSearch(ctx, args)
		if err == nil {
			return output, nil
		}
		// A canceled session must not start a second request.
		if ctx.Err() != nil {
			return "", wrapToolError(contracts.WebSearchTool, err, "search_failed", "the search could not be completed")
		}

		pause := time.NewTimer(ddgRequestPause)
		select {
		case <-ctx.Done():
			pause.Stop()
			return "", wrapToolError(contracts.WebSearchTool, err, "search_failed", "the search could not be completed")
		case <-pause.C:
		}

		fallback, fallbackErr := ddgSearch(ctx, args)
		if fallbackErr != nil {
			return "", wrapToolError(contracts.WebSearchTool, errors.Join(err, fallbackErr), "search_failed", "the search could not be completed by either search service")
		}
		return fallback, nil
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

type ddgInstantAnswer struct {
	Heading          string     `json:"Heading"`
	AbstractText     string     `json:"AbstractText"`
	AbstractSource   string     `json:"AbstractSource"`
	AbstractURL      string     `json:"AbstractURL"`
	Answer           string     `json:"Answer"`
	Definition       string     `json:"Definition"`
	DefinitionSource string     `json:"DefinitionSource"`
	DefinitionURL    string     `json:"DefinitionURL"`
	Redirect         string     `json:"Redirect"`
	Results          []ddgTopic `json:"Results"`
	RelatedTopics    []ddgTopic `json:"RelatedTopics"`
}

type ddgTopic struct {
	Text     string     `json:"Text"`
	FirstURL string     `json:"FirstURL"`
	Topics   []ddgTopic `json:"Topics"`
}

// ddgSearch queries DuckDuckGo's Instant Answer API. It returns summaries,
// instant answers and related topics rather than a full result listing; that is
// the only public DuckDuckGo API, and it is the fallback when Exa is unavailable
// or rate-limited.
func ddgSearch(ctx context.Context, args webSearchArguments) (string, error) {
	endpoint, err := url.Parse(ddgEndpoint)
	if err != nil {
		return "", filesystemErr("search_failed", "the search service could not be reached", contracts.ResponsibilityEnvironment, err)
	}
	query := endpoint.Query()
	query.Set("q", args.Query)
	query.Set("format", "json")
	query.Set("no_html", "1")
	// Return !bang destinations in the body instead of following an HTTP redirect.
	query.Set("no_redirect", "1")
	query.Set("t", ddgAppName)
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", filesystemErr("search_failed", "the search request could not be built", contracts.ResponsibilityEnvironment, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", ddgUserAgent)

	client := &http.Client{
		Timeout: ddgTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return "", filesystemErr("search_failed", "the search service could not be reached", contracts.ResponsibilityEnvironment, err)
	}
	defer response.Body.Close()

	raw, _, err := readCapped(response.Body, ddgMaxBody)
	if err != nil {
		return "", filesystemErr("search_failed", "the search response could not be read", contracts.ResponsibilityEnvironment, err)
	}
	// DuckDuckGo also answers with 202 while still returning a valid payload, so
	// the JSON decode below is the real check.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return "", filesystemErr("search_failed", fmt.Sprintf("the search service responded %s", response.Status), contracts.ResponsibilityEnvironment, nil)
	}

	var answer ddgInstantAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", filesystemErr("search_failed", "the search service returned an invalid response", contracts.ResponsibilityEnvironment, err)
	}

	text := formatDDGAnswer(answer, args)
	if text == "" {
		return "", filesystemErr("search_failed", "the search returned no usable result", contracts.ResponsibilityEnvironment, nil)
	}
	return clipWebContent(text, maxToolContentBytes), nil
}

func formatDDGAnswer(answer ddgInstantAnswer, args webSearchArguments) string {
	limit := exaMCPDefaultResults
	if args.NumResults != nil {
		limit = *args.NumResults
	}

	var out strings.Builder
	seen := make(map[string]bool)
	results := 0
	hasContent := false

	addEntry := func(text, firstURL string) {
		text = strings.TrimSpace(text)
		firstURL = strings.TrimSpace(firstURL)
		if results >= limit || text == "" || firstURL == "" || seen[firstURL] {
			return
		}
		seen[firstURL] = true
		fmt.Fprintf(&out, "- [%s](%s)\n", text, firstURL)
		results++
		hasContent = true
	}

	if text := strings.TrimSpace(answer.AbstractText); text != "" {
		if heading := strings.TrimSpace(answer.Heading); heading != "" {
			fmt.Fprintf(&out, "### %s\n\n", heading)
		}
		out.WriteString(text)
		out.WriteString("\n")
		if abstractURL := strings.TrimSpace(answer.AbstractURL); abstractURL != "" {
			seen[abstractURL] = true
			source := strings.TrimSpace(answer.AbstractSource)
			if source == "" {
				source = "source"
			}
			fmt.Fprintf(&out, "\nSource: [%s](%s)\n", source, abstractURL)
		}
		out.WriteString("\n")
		hasContent = true
	}

	if value := strings.TrimSpace(answer.Answer); value != "" {
		fmt.Fprintf(&out, "**Answer:** %s\n\n", value)
		hasContent = true
	}
	if value := strings.TrimSpace(answer.Definition); value != "" {
		if definitionURL := strings.TrimSpace(answer.DefinitionURL); definitionURL != "" {
			source := strings.TrimSpace(answer.DefinitionSource)
			if source == "" {
				source = "source"
			}
			fmt.Fprintf(&out, "**Definition:** %s — [%s](%s)\n\n", value, source, definitionURL)
		} else {
			fmt.Fprintf(&out, "**Definition:** %s\n\n", value)
		}
		hasContent = true
	}
	if redirect := strings.TrimSpace(answer.Redirect); redirect != "" {
		fmt.Fprintf(&out, "Redirect: %s\n\n", redirect)
		hasContent = true
	}

	for _, result := range answer.Results {
		addEntry(result.Text, result.FirstURL)
	}
	// Related topics are a flat list, or grouped under a topic with its own
	// Topics slice; walk the groups so every leaf link is considered.
	var walk func([]ddgTopic)
	walk = func(topics []ddgTopic) {
		for _, topic := range topics {
			if len(topic.Topics) > 0 {
				walk(topic.Topics)
				continue
			}
			addEntry(topic.Text, topic.FirstURL)
		}
	}
	walk(answer.RelatedTopics)

	if !hasContent {
		return ""
	}

	// DuckDuckGo's API terms require attribution in every place its results are
	// shown; the abstract also names its underlying source above.
	fmt.Fprintf(&out, "\nResults from [DuckDuckGo](https://duckduckgo.com/?q=%s)\n", url.QueryEscape(args.Query))
	return strings.TrimSpace(out.String()) + "\n"
}
