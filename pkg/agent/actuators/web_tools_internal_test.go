package actuators

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// allowLoopback disables the address blocklist so a test can point the fetch
// path at a local httptest server (which listens on 127.0.0.1).
func allowLoopback(t *testing.T) {
	t.Helper()
	saved := blockedAddressPrefixes
	blockedAddressPrefixes = nil
	t.Cleanup(func() { blockedAddressPrefixes = saved })
}

func newTestManager(t *testing.T) *FileManager {
	t.Helper()
	m, err := NewFileManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fetchCall(t *testing.T, arguments map[string]any) (string, error) {
	t.Helper()
	data, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return newTestManager(t).WebFetchExecution(context.Background(), contracts.ToolCall{
		Function: contracts.Function{Name: contracts.WebFetchTool, Arguments: string(data)},
	})
}

func TestWebClientBlocksPrivateAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "should never be reached")
	}))
	defer server.Close()

	// The blocklist is active here on purpose: a loopback target must be refused
	// before any connection is made.
	_, err := newWebClient().Get(server.URL)
	if !errors.Is(err, errWebAddressBlocked) {
		t.Fatalf("loopback fetch error = %v, want errWebAddressBlocked", err)
	}
}

func TestValidateWebURLRejectsBadRequests(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"https://",
		"http://user:pass@example.com/",
	} {
		if _, err := validateWebURL(raw); err == nil {
			t.Fatalf("validateWebURL(%q) = nil error, want rejection", raw)
		}
	}
	if _, err := validateWebURL("https://example.com/path"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
}

func TestClassifyWebContent(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		want        webContentKind
	}{
		{"html", "text/html; charset=utf-8", "<html><body>hi</body></html>", webContentHTML},
		{"xhtml", "application/xhtml+xml", "<html/>", webContentHTML},
		{"markdown", "text/markdown; charset=utf-8", "# hi", webContentMarkdown},
		{"json", "application/json", `{"a":1}`, webContentData},
		{"yaml", "application/yaml", "a: 1", webContentData},
		{"csv", "text/csv", "a,b\n1,2", webContentData},
		{"plain", "text/plain", "hello", webContentData},
		{"sniffed-html", "", "<!DOCTYPE html><html></html>", webContentHTML},
		{"sniffed-octet-html", "application/octet-stream", "<!DOCTYPE html><html></html>", webContentHTML},
		{"png", "image/png", "\x89PNG\r\n\x1a\n", webContentBinary},
		{"pdf", "application/pdf", "%PDF-1.4", webContentBinary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWebContent(tc.contentType, []byte(tc.body)); got != tc.want {
				t.Fatalf("classifyWebContent(%q) = %d, want %d", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestWebFetchConvertsHTML(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>T</title></head><body>
			<nav>menu junk</nav>
			<h1>Heading</h1>
			<p>Some <strong>bold</strong> text and a <a href="/x">link</a>.</p>
			<script>var x = 1;</script>
			</body></html>`)
	}))
	defer server.Close()

	out, err := fetchCall(t, map[string]any{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Heading") {
		t.Fatalf("output missing heading: %q", out)
	}
	if !strings.Contains(out, "**bold**") {
		t.Fatalf("output missing bold: %q", out)
	}
	if strings.Contains(out, "menu junk") || strings.Contains(out, "var x") {
		t.Fatalf("output kept stripped chrome: %q", out)
	}
}

func TestWebFetchPassesMarkdownThrough(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, "# Already markdown\n\nbody")
	}))
	defer server.Close()

	out, err := fetchCall(t, map[string]any{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out != "# Already markdown\n\nbody" {
		t.Fatalf("markdown passthrough = %q", out)
	}
}

func TestWebFetchTellsRawDataToUseCurl(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"a":1}`)
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil {
		t.Fatal("expected error for JSON response")
	}
	if !strings.Contains(err.Error(), "raw_data") || !strings.Contains(err.Error(), "curl") {
		t.Fatalf("error = %v, want raw_data pointing at curl", err)
	}
}

func TestWebFetchRejectsBinary(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "\x89PNG\r\n\x1a\n")
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "unsupported_content_type") {
		t.Fatalf("error = %v, want unsupported_content_type", err)
	}
}

func TestWebFetchReportsEmptyPage(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "no_content") {
		t.Fatalf("error = %v, want no_content", err)
	}
}

func TestWebSearchDrivesExaMCP(t *testing.T) {
	var sawSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)

		switch request.Method {
		case "initialize":
			w.Header().Set(exaMCPSessionHeader, "test-session")
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `event: message`+"\n"+`data: {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`+"\n\n")
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			sawSession = r.Header.Get(exaMCPSessionHeader)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `event: message`+"\n"+`data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"Title: X\nURL: https://x.example"}]}}`+"\n\n")
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	saved := exaMCPEndpoint
	exaMCPEndpoint = server.URL
	t.Cleanup(func() { exaMCPEndpoint = saved })

	out, err := newTestManager(t).WebSearchExecution(context.Background(), contracts.ToolCall{
		Function: contracts.Function{Name: contracts.WebSearchTool, Arguments: `{"query":"go net/http"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://x.example") {
		t.Fatalf("search output = %q", out)
	}
	if sawSession != "test-session" {
		t.Fatalf("tools/call session header = %q, want test-session", sawSession)
	}
}

func TestWebToolsLiveSmoke(t *testing.T) {
	if os.Getenv("INFAI_WEB_LIVE_TEST") == "" {
		t.Skip("set INFAI_WEB_LIVE_TEST=1 to run against live services")
	}

	searchOut, err := newTestManager(t).WebSearchExecution(context.Background(), contracts.ToolCall{
		Function: contracts.Function{Name: contracts.WebSearchTool, Arguments: `{"query":"Go net/http package","num_results":3}`},
	})
	if err != nil {
		t.Fatalf("live websearch failed: %v", err)
	}
	if !strings.Contains(searchOut, "http") {
		t.Fatalf("live websearch output = %q", searchOut)
	}

	fetchOut, err := fetchCall(t, map[string]any{"url": "https://dipankar-das.com/"})
	if err != nil {
		t.Fatalf("live webfetch failed: %v", err)
	}
	if !strings.Contains(fetchOut, "Dipankar") {
		t.Fatalf("live webfetch output = %q", fetchOut)
	}
}

func TestWebToolsHaveDescriptiveSchemas(t *testing.T) {
	for _, tool := range []contracts.Tool{WebFetchTool(), WebSearchTool()} {
		if tool.Name == "" || tool.Description == "" {
			t.Fatalf("tool = %+v, want name and description", tool)
		}
		if !json.Valid(tool.Parameters) || !strings.HasPrefix(string(tool.Parameters), `{"type":"object"`) {
			t.Fatalf("%s parameters = %s", tool.Name, tool.Parameters)
		}
	}
}

func TestWebFetchReportsHTTPStatus(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "<html><body><h1>Not found</h1></body></html>")
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "http_error") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want http_error naming the status", err)
	}
}

func TestWebFetchHonoursDeclaredCharset(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		body := append([]byte("<html><body><p>caf"), 0xe9) // 0xE9 is 'é' in latin-1
		body = append(body, []byte("</p></body></html>")...)
		w.Write(body)
	}))
	defer server.Close()

	out, err := fetchCall(t, map[string]any{"url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "café") {
		t.Fatalf("output = %q, want the declared charset decoded", out)
	}
}

func TestWebFetchRejectsUnsupportedRedirect(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid_redirect") {
		t.Fatalf("error = %v, want invalid_redirect", err)
	}
}

func TestWebFetchReportsOversizedBody(t *testing.T) {
	allowLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><body>")
		chunk := strings.Repeat("a", 1<<16)
		for written := 0; written <= webMaxBodyBytes; written += len(chunk) {
			io.WriteString(w, chunk)
		}
	}))
	defer server.Close()

	_, err := fetchCall(t, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "body_too_large") {
		t.Fatalf("error = %v, want body_too_large", err)
	}
}
