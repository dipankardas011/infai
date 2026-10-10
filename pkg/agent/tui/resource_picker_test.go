package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

func TestFuzzyResourceMatchesScoresURI(t *testing.T) {
	resources := []contracts.MCPResource{
		{URI: "file:///pkg/agent/tui/chat.go"},
		{URI: "file:///pkg/agent/tui/chat_test.go"},
		{URI: "https://example.com/README.md"},
	}
	got := fuzzyResourceMatches(resources, "chatg", 12)
	want := []contracts.MCPResource{resources[0], resources[1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches=%v want %v", got, want)
	}
}

func TestFuzzyResourceMatchesCapsResults(t *testing.T) {
	resources := make([]contracts.MCPResource, 20)
	for i := range resources {
		resources[i] = contracts.MCPResource{URI: fmt.Sprintf("file:///r%02d", i)}
	}
	if got := fuzzyResourceMatches(resources, "", 3); len(got) != 3 {
		t.Fatalf("matches=%d want 3", len(got))
	}
	p := &resourcePicker{resources: resources}
	p.filter("")
	if len(p.matches) != 12 {
		t.Fatalf("picker matches=%d want 12", len(p.matches))
	}
}

func TestRenderResourcePickerShowsNameAndURI(t *testing.T) {
	p := &resourcePicker{matches: []contracts.MCPResource{
		{Name: "docs index", URI: "file:///docs/index.md", MIMEType: "text/markdown", Size: 1536},
		{URI: "file:///docs/other.md"},
	}}
	out := ansi.Strip(renderResourcePicker(p, 80, newHarnessStyles()))
	if want := "› docs index · file:///docs/index.md · text/markdown · 1.5 KiB"; !strings.Contains(out, want) {
		t.Fatalf("picker=%q want row %q", out, want)
	}
	if want := "file:///docs/other.md · file:///docs/other.md"; !strings.Contains(out, want) {
		t.Fatalf("picker=%q want unnamed row %q", out, want)
	}
}

func TestRenderResourceBlocks(t *testing.T) {
	block := func(uri, body string) string {
		return `<infaiw_mcp_resource_get uri="` + uri + `">` + body + `</infaiw_mcp_resource_get>`
	}
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "success",
			text: "read " + block("file:///notes.md", "\nbody line\n") + " and continue",
			want: "read #file:///notes.md and continue",
		},
		{
			name: "unavailable",
			text: `look <infaiw_mcp_resource_get uri="file:///gone.md"/> please`,
			want: "look #file:///gone.md (unavailable) please",
		},
		{
			name: "escaped close in body",
			text: block("file:///doc.md", "\nsample \\</infaiw_mcp_resource_get>\nstill body"),
			want: "#file:///doc.md",
		},
		{
			name: "escaped open and close in body",
			text: block("file:///doc.md", "\nsample \\<infaiw_mcp_resource_get and \\</infaiw_mcp_resource_get>\nstill body"),
			want: "#file:///doc.md",
		},
		{
			name: "escaped open token is not an element",
			text: `\<infaiw_mcp_resource_get uri="file:///notreal"/> and real <infaiw_mcp_resource_get uri="file:///real"/>`,
			want: `\<infaiw_mcp_resource_get uri="file:///notreal"/> and real #file:///real (unavailable)`,
		},
		{
			name: "escaped uri",
			text: block("file:///a&amp;b&lt;c", "\nbody"),
			want: "#file:///a&b<c",
		},
		{
			name: "session encoder shape",
			text: "summary\n<infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" mime=\"text/markdown\">\n# Hello\n</infaiw_mcp_resource_get>",
			want: "summary\n#dummy://readme",
		},
		{
			name: "session encoder failure shape",
			text: "see <infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://gone\" error=\"mcp_resource_failed\"/>",
			want: "see #dummy://gone (unavailable)",
		},
		{
			name: "session encoder failure shape with explicit close tag",
			text: "see <infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://gone\" error=\"mcp_resource_failed\"></infaiw_mcp_resource_get> now",
			want: "see #dummy://gone (unavailable) now",
		},
		{
			name: "session encoder success shape with cdata body",
			text: "read <infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://readme\" mime=\"text/plain\"><![CDATA[dummy server readme]]></infaiw_mcp_resource_get> and continue",
			want: "read #dummy://readme and continue",
		},
		{
			name: "session encoder empty body without cdata",
			text: "read <infaiw_mcp_resource_get server=\"dummy\" uri=\"dummy://empty\"></infaiw_mcp_resource_get> and continue",
			want: "read #dummy://empty and continue",
		},
		{
			name: "split cdata body containing close sequence",
			text: "see <infaiw_mcp_resource_get uri=\"dummy://x\"><![CDATA[a ]]]]><![CDATA[> b]]></infaiw_mcp_resource_get> done",
			want: "see #dummy://x done",
		},
		{
			name: "cdata body containing the close token",
			text: "see <infaiw_mcp_resource_get uri=\"dummy://x\"><![CDATA[</infaiw_mcp_resource_get>]]></infaiw_mcp_resource_get> done",
			want: "see #dummy://x done",
		},
		{
			name: "attribute character references",
			text: "read <infaiw_mcp_resource_get uri=\"a&#34;b&#39;c&#x26;d\"><![CDATA[x]]></infaiw_mcp_resource_get> now",
			want: "read #a\"b'c&d now",
		},
		{
			name: "no block",
			text: "compare @a.go and @pkg/b.go",
			want: "compare @a.go and @pkg/b.go",
		},
		{
			name: "two blocks",
			text: block("file:///a.md", "\nA") + " and " + block("file:///b.md", "\nB"),
			want: "#file:///a.md and #file:///b.md",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderResourceBlocks(test.text); got != test.want {
				t.Fatalf("render=%q want %q", got, test.want)
			}
		})
	}
}

func TestHashOpensResourcePickerAndEnterInsertsURI(t *testing.T) {
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.session = store.SessionMeta{ID: uuid.New()}
	m.mcpResources = []contracts.MCPResource{{Server: "fs", Name: "notes", URI: "file:///notes.md"}}
	_ = m.composer.Focus()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '#', Text: "#"}))

	if m.resourcePicker == nil {
		t.Fatal("hash did not open the resource picker")
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got, want := m.composer.Value(), "#file:///notes.md"; got != want {
		t.Fatalf("composer=%q want %q", got, want)
	}
	if m.resourcePicker != nil {
		t.Fatal("enter left the resource picker open")
	}
}
