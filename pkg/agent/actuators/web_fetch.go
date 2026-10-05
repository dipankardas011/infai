package actuators

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// webAcceptHeader asks for agent-readable markdown first, then HTML. Sites
// enabled for "Markdown for Agents" answer with text/markdown; everyone else
// answers with HTML and the body is converted locally.
const webAcceptHeader = "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.8, */*;q=0.1"

// webUserAgent identifies infai so operators can see who is fetching.
const webUserAgent = "infai-webfetch/1.0"

// webStripSelectors removes page chrome before conversion. The library's base
// plugin already drops head/script/style; these are the layout elements it
// keeps that carry no page content.
const webStripSelectors = "script, style, noscript, template, svg, iframe, form, nav, footer, header, aside"

func WebFetchTool() contracts.Tool {
	return toolSchema(
		"webfetch",
		"Fetch a web page and return its content as markdown. Use for HTML pages and for sites that serve text/markdown to agents. Raw data (JSON, YAML, XML, CSV) and binary files are not returned — for those, use the bash tool with curl instead.",
		map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http:// or https:// URL",
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters to return; defaults to the tool output limit",
			},
		},
		[]string{"url"},
	)
}

type webFetchArguments struct {
	URL      string `json:"url"`
	MaxChars *int   `json:"max_chars,omitempty"`
}

type webContentKind int

const (
	webContentHTML webContentKind = iota
	webContentMarkdown
	webContentData
	webContentBinary
)

func (m *FileManager) WebFetchExecution(ctx context.Context, tc contracts.ToolCall) (string, error) {
	args, err := contracts.DecodeToolArguments[webFetchArguments](contracts.WebFetchTool, tc)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(args.URL) == "" {
		return "", contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_arguments", "webfetch requires a url", contracts.ResponsibilityAgent, nil)
	}
	if args.MaxChars != nil && *args.MaxChars < 1 {
		return "", contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_arguments", "max_chars must be positive", contracts.ResponsibilityAgent, nil)
	}

	return contracts.RunBounded(ctx, contracts.WebFetchTool, webClientTimeout+5*time.Second, func() (string, error) {
		output, err := fetchWebPage(ctx, args)
		if err != nil {
			return "", wrapToolError(contracts.WebFetchTool, err, "fetch_failed", "the page could not be fetched")
		}
		return output, nil
	})
}

func fetchWebPage(ctx context.Context, args webFetchArguments) (string, error) {
	target, err := validateWebURL(args.URL)
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", filesystemErr("invalid_url", "the URL could not be requested", contracts.ResponsibilityAgent, err)
	}
	request.Header.Set("Accept", webAcceptHeader)
	request.Header.Set("User-Agent", webUserAgent)

	response, err := newWebClient().Do(request)
	if err != nil {
		if errors.Is(err, errWebAddressBlocked) {
			return "", filesystemErr("blocked_url", "the URL resolves to a private or reserved address", contracts.ResponsibilityAgent, err)
		}
		// A redirect-guard rejection is already a tool error describing the
		// URL's fault; anything else is a transport failure.
		if toolErr, ok := errors.AsType[*contracts.ExecutionError](err); ok {
			return "", toolErr
		}
		return "", filesystemErr("fetch_failed", "the request could not be completed", contracts.ResponsibilityEnvironment, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", filesystemErr("http_error", fmt.Sprintf("the server returned %s", response.Status), contracts.ResponsibilityEnvironment, nil)
	}

	contentType := response.Header.Get("Content-Type")
	body, truncated, err := readCapped(response.Body, webMaxBodyBytes)
	if err != nil {
		return "", filesystemErr("fetch_failed", "the response body could not be read", contracts.ResponsibilityEnvironment, err)
	}
	if truncated {
		return "", filesystemErr("body_too_large", fmt.Sprintf("the page exceeds the %d byte fetch limit", int64(webMaxBodyBytes)), contracts.ResponsibilityTool, nil)
	}

	finalURL := target
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL
	}

	limit := maxToolContentBytes
	if args.MaxChars != nil && *args.MaxChars < limit {
		limit = *args.MaxChars
	}

	switch classifyWebContent(contentType, body) {
	case webContentMarkdown:
		content := strings.TrimSpace(string(body))
		if content == "" {
			return "", filesystemErr("no_content", "the server returned an empty markdown response", contracts.ResponsibilityTool, nil)
		}
		return clipWebContent(content, limit), nil
	case webContentHTML:
		return convertWebPage(ctx, finalURL, contentType, body, limit)
	case webContentData:
		return "", filesystemErr("raw_data", "this URL returns raw data, not a web page; use the bash tool with curl instead", contracts.ResponsibilityTool, nil)
	default:
		return "", filesystemErr("unsupported_content_type", "this URL returns a file type that cannot be rendered as a web page", contracts.ResponsibilityTool, nil)
	}
}

// classifyWebContent decides what a response is from its declared type, sniffing
// the first bytes when the server declares nothing usable. Only HTML and
// markdown are web pages; anything else is raw data (report it and point at
// curl) or an unsupported binary.
func classifyWebContent(contentType string, body []byte) webContentKind {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	if mediaType == "" || mediaType == "application/octet-stream" || mediaType == "binary/octet-stream" {
		sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(firstBytes(body, 512)))
		mediaType = strings.ToLower(strings.TrimSpace(sniffed))
	}

	switch {
	case mediaType == "text/html", mediaType == "application/xhtml+xml":
		return webContentHTML
	case mediaType == "text/markdown", mediaType == "text/x-markdown":
		return webContentMarkdown
	case isBinaryMediaType(mediaType):
		return webContentBinary
	default:
		return webContentData
	}
}

func isBinaryMediaType(mediaType string) bool {
	switch {
	case strings.HasPrefix(mediaType, "image/"),
		strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "video/"),
		strings.HasPrefix(mediaType, "font/"):
		return true
	}
	switch mediaType {
	case "application/octet-stream", "application/pdf", "application/zip", "application/gzip", "application/x-gzip",
		"application/x-tar", "application/x-7z-compressed", "application/x-rar-compressed",
		"application/wasm", "application/vnd.ms-fontobject":
		return true
	}
	return false
}

func convertWebPage(ctx context.Context, pageURL *url.URL, contentType string, body []byte, limit int) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", filesystemErr("no_content", "no readable text was extracted; the page may require JavaScript — try searching for the information instead", contracts.ResponsibilityTool, nil)
	}
	// Honour the charset the server declared; when it declared none,
	// charset.NewReader falls back to BOM and <meta> detection.
	if strings.TrimSpace(contentType) == "" {
		contentType = "text/html"
	}
	decoded, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", filesystemErr("extract_failed", "the page encoding could not be determined", contracts.ResponsibilityTool, err)
	}
	document, err := goquery.NewDocumentFromReader(decoded)
	if err != nil {
		return "", filesystemErr("extract_failed", "the page could not be parsed", contracts.ResponsibilityTool, err)
	}
	document.Find(webStripSelectors).Remove()
	cleaned, err := document.Html()
	if err != nil {
		return "", filesystemErr("extract_failed", "the page could not be prepared for conversion", contracts.ResponsibilityTool, err)
	}

	instance := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(commonmark.WithHorizontalRule("---")),
		table.NewTablePlugin(),
		strikethrough.NewStrikethroughPlugin(),
	))
	markdown, err := instance.ConvertString(cleaned, converter.WithContext(ctx), converter.WithDomain(originOf(pageURL)))
	if err != nil {
		return "", filesystemErr("extract_failed", "the page could not be converted to markdown", contracts.ResponsibilityTool, err)
	}

	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return "", filesystemErr("no_content", "no readable text was extracted; the page may require JavaScript — try searching for the information instead", contracts.ResponsibilityTool, nil)
	}
	return clipWebContent(markdown, limit), nil
}

func originOf(target *url.URL) string {
	if target == nil {
		return ""
	}
	return target.Scheme + "://" + target.Host
}
