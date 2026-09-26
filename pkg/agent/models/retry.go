package models

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

// defaultRetryPolicy suits HTTP endpoints that fail fast: enough attempts to
// ride out a restart or a brief rate limit, with a long enough cap that a
// provider asking for a minute gets it.
var defaultRetryPolicy = retryPolicy{maxAttempts: 10, base: 5 * time.Second, maxDelay: time.Minute}

// retryPolicy bounds how often a request may be attempted and how long to wait
// between attempts.
//
// "Request" here means the request and response-header phase only: bodies are
// streamed by the caller after send returns. A retry therefore can never
// re-deliver output the caller has already seen, which is what lets every
// provider adapter share one policy.
type retryPolicy struct {
	maxAttempts int
	base        time.Duration
	maxDelay    time.Duration
}

func (p retryPolicy) normalized() retryPolicy {
	if p.maxAttempts <= 0 {
		p.maxAttempts = 1
	}
	if p.base <= 0 {
		p.base = defaultRetryPolicy.base
	}
	if p.maxDelay <= 0 {
		p.maxDelay = defaultRetryPolicy.maxDelay
	}
	return p
}

// send runs build once per attempt until it returns a response, a fatal error,
// or the attempts run out, waiting between retryable failures. An error from
// build is a configuration failure — a missing credential, an unusable
// endpoint — and is never retried. label prefixes every error send returns.
func (p retryPolicy) send(ctx context.Context, label string, client *http.Client, opts *contracts.GenerateOptions, build func() (*http.Request, error)) (*http.Response, error) {
	p = p.normalized()

	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		switch {
		case err != nil:
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt == p.maxAttempts || !isRetryableTransportError(err) {
				return nil, fmt.Errorf("%s: request failed after %d attempt(s): %w", label, attempt, err)
			}
			if err := p.wait(ctx, attempt, 0, opts); err != nil {
				return nil, err
			}
		case resp.StatusCode != http.StatusOK:
			statusErr, retryAfter := readProviderError(label, resp)
			if attempt == p.maxAttempts || !isRetryableStatus(resp.StatusCode) {
				return nil, statusErr
			}
			if err := p.wait(ctx, attempt, retryAfter, opts); err != nil {
				return nil, err
			}
		default:
			return resp, nil
		}
	}
	return nil, fmt.Errorf("%s: retry loop exhausted", label)
}

// wait sleeps before the next attempt, announcing the delay through
// opts.OnDelta. Either kind of caller cancellation stops the retry: the context
// ending, or OnDelta reporting that the user cancelled.
func (p retryPolicy) wait(ctx context.Context, attempt int, retryAfter time.Duration, opts *contracts.GenerateOptions) error {
	delay := p.base
	for i := 1; i < attempt; i++ {
		if delay >= p.maxDelay>>1 {
			delay = p.maxDelay
			break
		}
		delay *= 2
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > p.maxDelay {
		delay = p.maxDelay
	}
	notice := fmt.Sprintf("LLM endpoint unavailable; retrying in %s (attempt %d/%d)", delay, attempt+1, p.maxAttempts)
	if opts != nil && opts.OnDelta != nil && opts.OnDelta(contracts.EventProviderEvent, notice) {
		return harnessErr.ErrTurnCanceled
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readProviderError drains and closes a non-200 response, returning the error a
// caller should see and the delay the provider asked for through Retry-After.
func readProviderError(label string, resp *http.Response) (error, time.Duration) {
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	closeErr := resp.Body.Close()

	statusErr := fmt.Errorf("%s: status %d: %s", label, resp.StatusCode, strings.TrimSpace(string(body)))
	if readErr != nil {
		statusErr = fmt.Errorf("%w: read response body: %v", statusErr, readErr)
	}
	if closeErr != nil {
		statusErr = fmt.Errorf("%w: close response body: %v", statusErr, closeErr)
	}
	return statusErr, retryAfter
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// isRetryableTransportError reports whether a failed request is worth another
// attempt.
//
// client.Do wraps every transport failure in *url.Error, and *url.Error carries
// a Timeout method, so it satisfies net.Error unconditionally. Testing the
// wrapper for net.Error therefore retries a certificate the machine does not
// trust, a redirect loop, or an unsupported scheme — each of which is a
// configuration mistake that only gets reported later. The unwrapped cause is
// what decides.
func isRetryableTransportError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}

	// An untrusted certificate does not become trusted on the next attempt.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}

	// A name that does not resolve will not start resolving either.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}

	// The connection died before a response arrived.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Dial, read, and write failures: refused, reset, unreachable, timed out.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	// Timeouts the transport reports through its own error types.
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}
