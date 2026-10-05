package actuators

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// Limits for the web tools. Fetching a model-chosen URL is bounded three ways:
// a client timeout bounds the whole request, a redirect cap bounds hop count,
// and a read cap bounds memory.
const (
	webClientTimeout = 30 * time.Second
	webDialTimeout   = 10 * time.Second
	webMaxRedirects  = 10
	webMaxBodyBytes  = 2 << 20
)

// errWebAddressBlocked identifies an SSRF rejection so the fetch path can report
// it as a blocked URL rather than a generic transport failure.
var errWebAddressBlocked = errors.New("web address is blocked")

// blockedAddressPrefixes are the ranges a model-chosen URL may never reach:
// loopback, RFC1918, link-local (including the cloud metadata address
// 169.254.169.254), unique-local, carrier-grade NAT, and reserved space.
var blockedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// newWebClient returns the client every outbound web request goes through. SSRF
// control runs in Dialer.Control, which fires after DNS resolution and before
// the connection is made: it sees the address actually being dialed, so a DNS
// answer that changes between a pre-flight check and the connect cannot bypass
// it. A custom Transport leaves Proxy nil, so environment proxy settings cannot
// route the request around the check either.
func newWebClient() *http.Client {
	dialer := &net.Dialer{Timeout: webDialTimeout, Control: guardDialControl}
	return &http.Client{
		Timeout: webClientTimeout,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
		CheckRedirect: guardRedirect,
	}
}

// guardDialControl rejects a connection whose resolved address is private or
// reserved. IPv4-mapped IPv6 is unmapped first, so ::ffff:127.0.0.1 cannot slip
// past an IPv4-only range.
func guardDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address", errWebAddressBlocked)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: unresolvable address", errWebAddressBlocked)
	}
	addr = addr.Unmap()
	for _, prefix := range blockedAddressPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: %s", errWebAddressBlocked, addr)
		}
	}
	return nil
}

// guardRedirect bounds redirect chains. Each hop still dials through
// guardDialControl, so a redirect to an internal host is blocked too. Failures
// are attributed to the agent: it chose the URL that redirected this way.
func guardRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= webMaxRedirects {
		return contracts.NewToolExecutionError(contracts.WebFetchTool, "too_many_redirects", fmt.Sprintf("the URL redirected more than %d times", webMaxRedirects), contracts.ResponsibilityAgent, nil)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_redirect", fmt.Sprintf("the URL redirected to an unsupported scheme %q", req.URL.Scheme), contracts.ResponsibilityAgent, nil)
	}
	if req.URL.User != nil {
		return contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_redirect", "the URL redirected to a location with embedded credentials", contracts.ResponsibilityAgent, nil)
	}
	return nil
}

// validateWebURL enforces the request shape before any network I/O: absolute
// http(s), a host, and no embedded credentials.
func validateWebURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_url", "the URL could not be parsed", contracts.ResponsibilityAgent, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_url", "the URL must use http or https", contracts.ResponsibilityAgent, nil)
	}
	if parsed.Hostname() == "" {
		return nil, contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_url", "the URL must include a host", contracts.ResponsibilityAgent, nil)
	}
	if parsed.User != nil {
		return nil, contracts.NewToolExecutionError(contracts.WebFetchTool, "invalid_url", "the URL must not embed credentials", contracts.ResponsibilityAgent, nil)
	}
	return parsed, nil
}

// readCapped reads at most limit bytes and reports whether the reader still had
// more, so a caller can tell a complete body from a truncated one.
func readCapped(reader io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// truncateUTF8 cuts at a rune boundary so the returned text is always valid
// UTF-8, and reports whether it had to cut.
func truncateUTF8(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}

// clipWebContent bounds tool output to the harness limit and marks a cut so the
// model knows it did not receive the whole page.
func clipWebContent(content string, limit int) string {
	clipped, truncated := truncateUTF8(content, limit)
	if !truncated {
		return clipped
	}
	return clipped + "\n\n[content truncated]"
}

func firstBytes(body []byte, count int) []byte {
	if len(body) <= count {
		return body
	}
	return body[:count]
}
