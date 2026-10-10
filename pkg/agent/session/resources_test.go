package session

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func summarizeResourceReferences(refs []resourceReference) []string {
	summary := make([]string, 0, len(refs))
	for _, ref := range refs {
		summary = append(summary, fmt.Sprintf("%d:%d:%s", ref.start, ref.end, ref.uri))
	}
	return summary
}

// findResourceReferenceCases is the table behind TestFindResourceReferences.
var findResourceReferenceCases = []struct {
	name string
	text string
	want []string
}{
	{name: "at start of text", text: "#dummy://readme", want: []string{"0:15:dummy://readme"}},
	{name: "after a space", text: "see #dummy://readme", want: []string{"4:19:dummy://readme"}},
	{name: "after a newline", text: "see\n#dummy://readme", want: []string{"4:19:dummy://readme"}},
	{name: "after a tab", text: "see\t#dummy://readme", want: []string{"4:19:dummy://readme"}},
	{name: "two references", text: "#dummy://one\n#dummy://two", want: []string{"0:12:dummy://one", "13:25:dummy://two"}},
	{name: "trailing punctuation trimmed", text: "#dummy://readme).", want: []string{"0:15:dummy://readme"}},
	{name: "trailing quote trimmed", text: "#dummy://readme\"", want: []string{"0:15:dummy://readme"}},
	// A resource URI may use any protocol, so the catalogue, not the token's
	// shape, is what makes these references. § go-sdk ReadResourceParams
	{name: "catalogue URI without a scheme separator", text: "#urn:isbn:12345", want: []string{"0:15:urn:isbn:12345"}},
	{name: "catalogue URI with a colon", text: "#grafana:panel/7", want: []string{"0:16:grafana:panel/7"}},
	{name: "catalogue URI with trailing punctuation", text: "#urn:isbn:12345.", want: []string{"0:15:urn:isbn:12345"}},
	{name: "unoffered URI shape", text: "#unknown://thing", want: []string{"0:16:unknown://thing"}},
	// Unicode spaces end a token, so a pasted CR or a non-breaking space is not
	// swallowed into the URI and the exact lookup still matches.
	{name: "windows line ending ends the token", text: "#dummy://readme\r\n", want: []string{"0:15:dummy://readme"}},
	{name: "non-breaking space ends the token", text: "see #dummy://readme\u00a0rest", want: []string{"4:19:dummy://readme"}},
	{name: "hash inside a word", text: "a#b://c", want: nil},
	{name: "lone hash", text: "just a # here", want: nil},
	{name: "scheme separator required", text: "#dummy", want: nil},
	{name: "issue number", text: "#123 fixed it", want: nil},
	{name: "markdown heading", text: "## Plan", want: nil},
	{name: "url fragment", text: "see https://example.com/x#frag", want: nil},
	{name: "no references", text: "plain text", want: nil},
}

func TestFindResourceReferences(t *testing.T) {
	known := knownURIs("dummy://readme", "dummy://one", "dummy://two", "urn:isbn:12345", "grafana:panel/7")
	for _, tt := range findResourceReferenceCases {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeResourceReferences(findResourceReferences(tt.text, known))
			if len(got) != len(tt.want) {
				t.Fatalf("findResourceReferences(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("findResourceReferences(%q) = %v, want %v", tt.text, got, tt.want)
				}
			}
		})
	}
}

// knownURIs is the catalogue predicate the scanner gets in tests: the URIs a
// server offered are the ones it can read.
func knownURIs(uris ...string) func(string) bool {
	offered := make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		offered[uri] = struct{}{}
	}
	return func(uri string) bool {
		_, ok := offered[uri]
		return ok
	}
}

func TestExpandResourceReferencesPlacement(t *testing.T) {
	read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		return contracts.MCPResourceRead{Server: "dummy", URI: uri, MIMEType: "text/markdown", Text: "# Hello"}, nil
	}
	servers := map[string]string{"dummy://readme": "dummy"}
	block := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" mime=\"text/markdown\"><![CDATA[# Hello]]></infaiw_mcp_resource_get>"

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "at start of text", text: "#dummy://readme", want: block},
		{name: "after a space", text: "see #dummy://readme", want: "see \n" + block},
		{name: "after a newline", text: "see\n#dummy://readme", want: "see\n" + block},
		{name: "trailing punctuation left intact", text: "#dummy://readme.", want: block + "."},
		{name: "word hash untouched", text: "a#b://c", want: "a#b://c"},
		{name: "separator required", text: "#dummy", want: "#dummy"},
		{name: "lone hash untouched", text: "just a #", want: "just a #"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandResourceReferencesWith(context.Background(), tt.text, servers, read, knownURIs())
			if got != tt.want {
				t.Fatalf("expandResourceReferencesWith(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestExpandResourceReferencesTwoReferences(t *testing.T) {
	read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		return contracts.MCPResourceRead{Server: "dummy", URI: uri, Text: uri}, nil
	}
	text := "#dummy://one\n#dummy://two"
	first := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://one\"><![CDATA[dummy://one]]></infaiw_mcp_resource_get>"
	second := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://two\"><![CDATA[dummy://two]]></infaiw_mcp_resource_get>"

	got := expandResourceReferencesWith(context.Background(), text, nil, read, knownURIs())
	if want := first + "\n" + second; got != want {
		t.Fatalf("two references = %q, want %q", got, want)
	}
}

func TestExpandResourceReferencesRepeatedURI(t *testing.T) {
	calls := 0
	read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		calls++
		return contracts.MCPResourceRead{Server: "dummy", URI: uri, Text: "body"}, nil
	}
	text := "#dummy://one\n#dummy://one"
	block := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://one\"><![CDATA[body]]></infaiw_mcp_resource_get>"

	got := expandResourceReferencesWith(context.Background(), text, nil, read, knownURIs())
	if want := block + "\n" + block; got != want {
		t.Fatalf("repeated URI = %q, want %q", got, want)
	}
	if calls != 1 {
		t.Fatalf("read calls = %d, want 1", calls)
	}
}

// TestExpandResourceReferencesErrorForm covers a read that fails with an error
// carrying no code: the reason is the generic one, not err.Error().
func TestExpandResourceReferencesErrorForm(t *testing.T) {
	failing := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		return contracts.MCPResourceRead{}, errors.New("unknown_resource")
	}

	tests := []struct {
		name    string
		servers map[string]string
		want    string
	}{
		{
			name: "unknown server omitted",
			want: "<infaiw_mcp_resource_get uri=\"dummy://x\" error=\"the MCP resource could not be read\"></infaiw_mcp_resource_get>",
		},
		{
			name:    "known server attributed",
			servers: map[string]string{"dummy://x": "dummy"},
			want:    "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://x\" error=\"the MCP resource could not be read\"></infaiw_mcp_resource_get>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandResourceReferencesWith(context.Background(), "#dummy://x", tt.servers, failing, knownURIs())
			if got != tt.want {
				t.Fatalf("error form = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("execution error reason is escaped", func(t *testing.T) {
		read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
			return contracts.MCPResourceRead{}, contracts.NewToolExecutionError(contracts.ToolType(uri), "mcp_resource_failed", `bad "reason" & <more>`, contracts.ResponsibilityEnvironment, nil)
		}
		got := expandResourceReferencesWith(context.Background(), "#dummy://x", nil, read, knownURIs())
		want := "<infaiw_mcp_resource_get uri=\"dummy://x\" error=\"mcp_resource_failed: bad &#34;reason&#34; &amp; &lt;more&gt;\"></infaiw_mcp_resource_get>"
		if got != want {
			t.Fatalf("escaped reason = %q, want %q", got, want)
		}
	})
}

func TestResourceFailureReason(t *testing.T) {
	execution := contracts.NewToolExecutionError(contracts.ToolType("dummy://nope"), "mcp_resource_failed",
		"the MCP server could not read the resource", contracts.ResponsibilityEnvironment, nil)
	if got, want := resourceFailureReason(execution), "mcp_resource_failed: the MCP server could not read the resource"; got != want {
		t.Fatalf("execution error reason = %q, want %q", got, want)
	}
	if got, want := resourceFailureReason(errors.New("boom")), "the MCP resource could not be read"; got != want {
		t.Fatalf("plain error reason = %q, want %q", got, want)
	}
}

func TestResolveResourceBlockBudget(t *testing.T) {
	readCalls := 0
	read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		readCalls++
		return contracts.MCPResourceRead{Server: "dummy", URI: uri, Text: "body"}, nil
	}

	block, bodyBytes := resolveResourceBlock(context.Background(), "dummy://x", "dummy", maxMessageResourceBytes, read)
	want := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://x\" error=\"message resource budget exceeded\"></infaiw_mcp_resource_get>"
	if block != want {
		t.Fatalf("budget block = %q, want %q", block, want)
	}
	if bodyBytes != 0 {
		t.Fatalf("budget bodyBytes = %d, want 0", bodyBytes)
	}
	if readCalls != 0 {
		t.Fatalf("budget read calls = %d, want 0", readCalls)
	}

	readCalls = 0
	if _, bodyBytes = resolveResourceBlock(context.Background(), "dummy://x", "dummy", maxMessageResourceBytes-1, read); bodyBytes != len("body") {
		t.Fatalf("under budget bodyBytes = %d, want %d", bodyBytes, len("body"))
	}
	if readCalls != 1 {
		t.Fatalf("under budget read calls = %d, want 1", readCalls)
	}
}

func TestExpandResourceReferencesByteBudget(t *testing.T) {
	body := strings.Repeat("a", maxResourceBodyBytes)
	readCalls := 0
	read := func(_ context.Context, uri string) (contracts.MCPResourceRead, error) {
		readCalls++
		return contracts.MCPResourceRead{Server: "dummy", URI: uri, Text: body}, nil
	}
	text := "#dummy://0\n#dummy://1\n#dummy://2\n#dummy://3\n#dummy://4"

	got := expandResourceReferencesWith(context.Background(), text, nil, read, knownURIs())
	if !strings.HasSuffix(got, "<infaiw_mcp_resource_get uri=\"dummy://4\" error=\"message resource budget exceeded\"></infaiw_mcp_resource_get>") {
		t.Fatalf("fifth reference did not switch to the budget error form: %q", got)
	}
	if readCalls != 4 {
		t.Fatalf("read calls = %d, want 4", readCalls)
	}
}

func TestResourceSuccessBlockFormat(t *testing.T) {
	got := resourceSuccessBlock("dummy", "dummy://readme", "text/markdown", "body")
	want := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" mime=\"text/markdown\"><![CDATA[body]]></infaiw_mcp_resource_get>"
	if got != want {
		t.Fatalf("success block = %q, want %q", got, want)
	}

	got = resourceSuccessBlock("", "dummy://readme", "", "body")
	want = "<infaiw_mcp_resource_get uri=\"dummy://readme\"><![CDATA[body]]></infaiw_mcp_resource_get>"
	if got != want {
		t.Fatalf("success block without server or mime = %q, want %q", got, want)
	}
}

func TestResourceErrorBlockFormat(t *testing.T) {
	got := resourceErrorBlock("dummy", "dummy://readme", "mcp_resource_failed")
	want := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" error=\"mcp_resource_failed\"></infaiw_mcp_resource_get>"
	if got != want {
		t.Fatalf("error block = %q, want %q", got, want)
	}
}

// TestResourceBlockIsWellFormedXML guards the design decision behind using the
// XML marshaller: attributes are escaped, a body reaches the model verbatim
// inside CDATA, and a body can never end the element early because Go splits a
// `]]>` across two CDATA sections.
func TestResourceBlockIsWellFormedXML(t *testing.T) {
	t.Run("exact bytes", func(t *testing.T) {
		success := resourceSuccessBlock("dummy", "dummy://readme", "text/plain", "body")
		wantSuccess := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" mime=\"text/plain\"><![CDATA[body]]></infaiw_mcp_resource_get>"
		if success != wantSuccess {
			t.Fatalf("success = %q, want %q", success, wantSuccess)
		}
		failure := resourceErrorBlock("dummy", "dummy://readme", "mcp_resource_failed")
		wantFailure := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" error=\"mcp_resource_failed\"></infaiw_mcp_resource_get>"
		if failure != wantFailure {
			t.Fatalf("failure = %q, want %q", failure, wantFailure)
		}
	})

	t.Run("body is verbatim inside CDATA", func(t *testing.T) {
		body := "raw \" & <b>x</b> ` \t \r </infaiw_mcp_resource_get> #dummy://other"
		block := resourceSuccessBlock("dummy", "dummy://readme", "text/plain", body)
		if !strings.Contains(block, "<![CDATA["+body+"]]>") {
			t.Fatalf("body was not written verbatim inside CDATA: %q", block)
		}
	})

	t.Run("close sequence round-trips", func(t *testing.T) {
		body := "a ]]> b"
		block := resourceSuccessBlock("dummy", "dummy://readme", "", body)
		var decoded struct {
			Body string `xml:",chardata"`
		}
		if err := xml.Unmarshal([]byte(block), &decoded); err != nil {
			t.Fatalf("unmarshal(%q) failed: %v", block, err)
		}
		if decoded.Body != body {
			t.Fatalf("round-tripped body = %q, want %q", decoded.Body, body)
		}
	})

	t.Run("attribute double quote uses a character reference", func(t *testing.T) {
		block := resourceSuccessBlock("dummy", `dummy://a"b`, "", "body")
		want := "<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://a&#34;b\"><![CDATA[body]]></infaiw_mcp_resource_get>"
		if block != want {
			t.Fatalf("quoted attribute = %q, want %q", block, want)
		}
	})
}

func TestClipResourceBody(t *testing.T) {
	short := strings.Repeat("a", maxResourceBodyBytes)
	if got := clipResourceBody(short); got != short {
		t.Fatalf("body at the limit was clipped: len=%d", len(got))
	}

	long := strings.Repeat("a", maxResourceBodyBytes-1) + "é" + "tail"
	got := clipResourceBody(long)
	const marker = "\n\n[content truncated]"
	if !strings.HasSuffix(got, marker) {
		t.Fatalf("clipped body missing marker: %q", got)
	}
	clipped := strings.TrimSuffix(got, marker)
	if len(clipped) != maxResourceBodyBytes-1 {
		t.Fatalf("clipped body length = %d, want %d", len(clipped), maxResourceBodyBytes-1)
	}
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipped body is not valid UTF-8: %q", clipped)
	}
}
