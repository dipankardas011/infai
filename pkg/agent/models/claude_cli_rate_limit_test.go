package models

import (
	"context"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// providerNotes collects the provider-facing notices emitted while a stream is
// decoded, so a test can assert what the user was told.
func providerNotes(t *testing.T, stream string) (claudeStreamResult, error, []string) {
	t.Helper()
	var notes []string
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), &contracts.GenerateOptions{
		Stream: true,
		OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			if kind == contracts.EventProviderEvent {
				notes = append(notes, text)
			}
			return false
		},
	}, nil, nil)
	return result, err, notes
}

// TestClaudeRateLimitRejectionNamesWindowUsageAndReset pins the hard-error path:
// a rejected window the account cannot cover fails the turn, and the message
// carries the data the CLI actually sent (window, utilization, epoch reset).
func TestClaudeRateLimitRejectionNamesWindowUsageAndReset(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790578200,"rateLimitType":"five_hour","utilization":1.0,"overageStatus":"rejected","isUsingOverage":false}}`,
		`{"type":"result","is_error":true,"subtype":"success","result":"Claude AI usage limit reached"}`,
	}, "\n")

	result, err, notes := providerNotes(t, stream)
	if err == nil {
		t.Fatal("a rejected window did not fail the turn")
	}
	if result.RateLimit == "" {
		t.Fatal("the rate limit was not recorded on the result")
	}
	for _, want := range []string{"Claude subscription limit reached", "5-hour limit", "100% used", "resets"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "5-hour") {
		t.Fatalf("notes = %v, want a window-naming provider notice", notes)
	}
}

// TestClaudeRateLimitWarningDoesNotFailTheTurn pins the distinction between a
// warning and a wall: allowed_warning is informational and the answer still
// commits. Before this, any non-allowed status was reported as "limit reached".
func TestClaudeRateLimitWarningDoesNotFailTheTurn(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1790578200,"rateLimitType":"five_hour","utilization":0.85}}`,
		`{"type":"result","structured_output":{"content":"answer","tool_calls":[]}}`,
	}, "\n")

	result, err, notes := providerNotes(t, stream)
	if err != nil {
		t.Fatalf("a warning failed the turn: %v", err)
	}
	if result.RateLimit != "" {
		t.Fatalf("RateLimit = %q, want empty on a warning", result.RateLimit)
	}
	if result.Output == nil || result.Output.Content == nil || *result.Output.Content != "answer" {
		t.Fatalf("output = %+v, want the committed answer", result.Output)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one warning notice", notes)
	}
	if strings.Contains(notes[0], "limit reached") {
		t.Fatalf("warning = %q, must not claim the limit was reached", notes[0])
	}
	for _, want := range []string{"usage is high", "85% used", "resets"} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("warning = %q, want it to contain %q", notes[0], want)
		}
	}
}

// TestClaudeRateLimitCoveredByOverageIsNotFatal pins that a rejection paid for
// by extra usage is a notice, not a failure.
func TestClaudeRateLimitCoveredByOverageIsNotFatal(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790578200,"rateLimitType":"five_hour","utilization":1.0,"isUsingOverage":true}}`,
		`{"type":"result","structured_output":{"content":"answer","tool_calls":[]}}`,
	}, "\n")

	result, err, notes := providerNotes(t, stream)
	if err != nil {
		t.Fatalf("an overage-covered rejection failed the turn: %v", err)
	}
	if result.RateLimit != "" {
		t.Fatalf("RateLimit = %q, want empty when extra usage covers it", result.RateLimit)
	}
	if result.Output == nil || result.Output.Content == nil || *result.Output.Content != "answer" {
		t.Fatalf("output = %+v, want the committed answer", result.Output)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "extra usage") {
		t.Fatalf("notes = %v, want one notice naming extra usage", notes)
	}
}

// TestClaudeRateLimitResetAcceptsSecondsAndMilliseconds pins the unit guard so a
// CLI that switches to millisecond epochs cannot produce a date far in the
// future.
func TestClaudeRateLimitResetAcceptsSecondsAndMilliseconds(t *testing.T) {
	seconds := claudeRateLimitReset(1790578200, "|")
	milliseconds := claudeRateLimitReset(1790578200000, "|")
	if seconds == "" {
		t.Fatal("a valid second epoch produced no reset")
	}
	if !strings.HasPrefix(seconds, "|") {
		t.Fatalf("reset = %q, want the prefix preserved", seconds)
	}
	if seconds != milliseconds {
		t.Fatalf("seconds = %q, milliseconds = %q, want the same instant", seconds, milliseconds)
	}
	if got := claudeRateLimitReset(0, "|"); got != "" {
		t.Fatalf("zero epoch = %q, want an omitted reset", got)
	}
}

// TestClaudeRateLimitWindowsAreHumanized pins the labels for the window kinds
// the CLI names.
func TestClaudeRateLimitWindowsAreHumanized(t *testing.T) {
	cases := map[string]string{
		"five_hour":                  "5-hour",
		"seven_day":                  "weekly",
		"seven_day_opus":             "weekly (Opus)",
		"seven_day_overage_included": "weekly (with extra usage)",
		"overage":                    "extra usage",
	}
	for kind, want := range cases {
		if got := claudeRateLimitWindow(kind); got != want {
			t.Fatalf("window(%q) = %q, want %q", kind, got, want)
		}
	}
}
