package session

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// Resource bodies are bounded per read and per user message, so one reference
// cannot hand the model an unbounded amount of text.
const (
	maxResourceBodyBytes    = 64 << 10
	maxMessageResourceBytes = 256 << 10
)

const (
	// resourceURIStops are the trailing punctuation bytes that stay in the
	// message rather than becoming part of a URI.
	resourceURIStops = ".,;:!?)]}\">\"'"

	resourceBudgetReason = "message resource budget exceeded"
)

// resourceBlock is the element the session writes for one `#uri` reference.
// The marshaller escapes the attribute values and wraps the body in CDATA,
// where Go splits every `]]>` across two CDATA sections, so a body can never
// end the element early — which is why the body needs no escaping of its own.
// § encoding/xml marshal.go ,attr/,cdata
type resourceBlock struct {
	XMLName xml.Name `xml:"infaiw_mcp_resource_get"`
	Server  string   `xml:"server,attr,omitempty"`
	URI     string   `xml:"uri,attr,omitempty"`
	MIME    string   `xml:"mime,attr,omitempty"`
	Error   string   `xml:"error,attr,omitempty"`
	Body    string   `xml:",cdata"`
}

// resourceReference is one `#uri` occurrence in a user message. The span runs
// from the `#` through the end of the URI; the punctuation trimmed off the
// token is left outside the span.
type resourceReference struct {
	start int
	end   int
	uri   string
}

type resourceReaderFunc func(ctx context.Context, uri string) (contracts.MCPResourceRead, error)

// expandResourceReferences translates every MCP resource reference in a user
// message into an XML block carrying the resource body. It never returns an
// error: a read that fails or overruns the message budget degrades to an error
// block so the message still reaches the agent.
func (s *InfaiAgentSession) expandResourceReferences(ctx context.Context, text string) string {
	foundReferences := findResourceReferences(text, s.mcpManager.HasResource)
	if len(foundReferences) == 0 {
		return text
	}
	return expandReferences(ctx, text, foundReferences, resourceServers(s.mcpManager.Resources()), s.mcpManager.ReadResource)
}

// expandResourceReferencesWith is the test seam: it scans with the caller's
// catalogue predicate and expands without a manager.
func expandResourceReferencesWith(ctx context.Context, text string, servers map[string]string, read resourceReaderFunc, known func(string) bool) string {
	return expandReferences(ctx, text, findResourceReferences(text, known), servers, read)
}

func expandReferences(ctx context.Context, text string, references []resourceReference, servers map[string]string, read resourceReaderFunc) string {
	if len(references) == 0 {
		return text
	}

	type cachedBlock struct {
		text      string
		bodyBytes int
	}
	cache := make(map[string]cachedBlock, len(references))
	emittedBodyBytes := 0

	var out strings.Builder
	out.Grow(len(text))
	cursor := 0
	for _, reference := range references {
		out.WriteString(text[cursor:reference.start])
		// A block must begin its own line, so a mid-line reference gets a
		// newline in front of it.
		if reference.start > 0 && text[reference.start-1] != '\n' {
			out.WriteByte('\n')
		}
		block, ok := cache[reference.uri]
		if !ok {
			block.text, block.bodyBytes = resolveResourceBlock(ctx, reference.uri, servers[reference.uri], emittedBodyBytes, read)
			cache[reference.uri] = block
		}
		emittedBodyBytes += block.bodyBytes
		out.WriteString(block.text)
		cursor = reference.end
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// resolveResourceBlock produces one block and reports how many body bytes it
// emits, so the caller can keep the per-message budget.
func resolveResourceBlock(ctx context.Context, uri, server string, emittedBodyBytes int, read resourceReaderFunc) (string, int) {
	if emittedBodyBytes >= maxMessageResourceBytes {
		return resourceErrorBlock(server, uri, resourceBudgetReason), 0
	}
	result, err := read(ctx, uri)
	if err != nil {
		return resourceErrorBlock(server, uri, resourceFailureReason(err)), 0
	}
	blockServer := result.Server
	if blockServer == "" {
		blockServer = server
	}
	body := clipResourceBody(result.Text)
	return resourceSuccessBlock(blockServer, uri, result.MIMEType, body), len(body)
}

// resourceFailureReason keeps a failure short: the execution error's code and
// reason are what a reader of the message needs, not the harness framing.
func resourceFailureReason(err error) string {
	if execution, ok := errors.AsType[*contracts.ExecutionError](err); ok {
		return execution.Code + ": " + execution.Reason
	}
	return "the MCP resource could not be read"
}

// resourceServers maps each catalogue URI to its server, for attributing a
// failure or a budget skip to the server that owns the resource.
func resourceServers(resources []contracts.MCPResource) map[string]string {
	servers := make(map[string]string, len(resources))
	for _, resource := range resources {
		if _, ok := servers[resource.URI]; !ok {
			servers[resource.URI] = resource.Server
		}
	}
	return servers
}

// findResourceReferences scans for `#uri` references. A `#` starts one only at
// the very start of the text or right after a whitespace byte; the token runs
// to the next whitespace byte. A token is a reference when the catalogue can
// read it or when it looks like a URI: a server may use any protocol in a
// resource URI, and an unoffered URI-shaped token still reports itself as
// unavailable rather than silently staying text. Token boundaries are Unicode
// spaces, so a pasted CR, a non-breaking space or an ideographic space ends the
// URI instead of being swallowed into it. § unicode.IsSpace
func findResourceReferences(text string, known func(string) bool) []resourceReference {
	var references []resourceReference
	for index := 0; index < len(text); index++ {
		if text[index] != '#' {
			continue
		}
		if index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:index])
			if !unicode.IsSpace(previous) {
				continue
			}
		}
		end := len(text)
		if next := strings.IndexFunc(text[index+1:], unicode.IsSpace); next >= 0 {
			end = index + 1 + next
		}
		uri := strings.TrimRight(text[index+1:end], resourceURIStops)
		if uri == "" || (!strings.Contains(uri, "://") && (known == nil || !known(uri))) {
			continue
		}
		references = append(references, resourceReference{start: index, end: index + 1 + len(uri), uri: uri})
		index = end - 1
	}
	return references
}

// resourceRenderingFailureBlock is the element a marshal failure degrades to.
// Marshalling these string fields cannot fail, but a failure must never panic
// inside a message send.
const resourceRenderingFailureBlock = `<infaiw_mcp_resource_get error="resource block rendering failed"/>`

func resourceSuccessBlock(server, uri, mime, body string) string {
	return marshalResourceBlock(resourceBlock{Server: server, URI: uri, MIME: mime, Body: body})
}

func resourceErrorBlock(server, uri, reason string) string {
	return marshalResourceBlock(resourceBlock{Server: server, URI: uri, Error: reason})
}

func marshalResourceBlock(block resourceBlock) string {
	encoded, err := xml.Marshal(block)
	if err != nil {
		return resourceRenderingFailureBlock
	}
	return string(encoded)
}

// clipResourceBody bounds the body and marks the cut the way the web tools do.
func clipResourceBody(body string) string {
	clipped, truncated := truncateResourceBody(body, maxResourceBodyBytes)
	if !truncated {
		return clipped
	}
	return clipped + "\n\n[content truncated]"
}

func truncateResourceBody(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}
