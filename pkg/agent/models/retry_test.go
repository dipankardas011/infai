package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

const codexCompletedSSE = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
const chatCompletionSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

// fastPolicy keeps retry tests off the real backoff schedule.
func fastPolicy() retryPolicy {
	return retryPolicy{maxAttempts: 3, base: time.Millisecond, maxDelay: time.Millisecond}
}

func textOnlyModel(id string) contracts.LLMModelConfiguration {
	return contracts.LLMModelConfiguration{Id: id, Modality: []contracts.LLMSupportedModality{contracts.ModalityText}}
}

func codexModel(endpoint string) contracts.ProvisionedModel {
	return contracts.NewProvisionedModel(
		contracts.Codex, "openai-codex", endpoint, contracts.OpenAICodexResponsesAPI,
		contracts.LLMProviderAuth{Method: contracts.OAuth2, AccessToken: "access-token", AccountID: "account-id"},
		textOnlyModel("gpt-5-codex"),
	)
}

// TestProviderRequestsRetryTransientFailures is the cross-provider contract:
// every adapter retries the same transient status and ends up with one copy of
// the streamed answer.
func TestProviderRequestsRetryTransientFailures(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		build func(endpoint string) (contracts.InfaiModelAdaptor, *retryPolicy)
	}{
		{
			name: "generic openai",
			body: chatCompletionSSE,
			build: func(endpoint string) (contracts.InfaiModelAdaptor, *retryPolicy) {
				api, err := NewOpenAICompatableAPI(contracts.NewProvisionedModel(
					contracts.OpenAIGeneric, "openai-generic", endpoint, contracts.OpenAICompatableAPI,
					contracts.LLMProviderAuth{Method: contracts.APIKey, BearerToken: "key"}, textOnlyModel("gpt-4o"),
				))
				if err != nil {
					t.Fatal(err)
				}
				return api, &api.retry
			},
		},
		{
			name: "deepseek",
			body: chatCompletionSSE,
			build: func(endpoint string) (contracts.InfaiModelAdaptor, *retryPolicy) {
				api, err := NewDeepSeekAPI(contracts.NewProvisionedModel(
					contracts.DeepSeek, "deepseek", endpoint, contracts.OpenAICompatableAPI,
					contracts.LLMProviderAuth{Method: contracts.APIKey, BearerToken: "key"}, textOnlyModel("deepseek-chat"),
				))
				if err != nil {
					t.Fatal(err)
				}
				return api, &api.transport.retry
			},
		},
		{
			name: "codex",
			body: codexCompletedSSE,
			build: func(endpoint string) (contracts.InfaiModelAdaptor, *retryPolicy) {
				api, err := NewOpenAICodexResponsesAPI(codexModel(endpoint))
				if err != nil {
					t.Fatal(err)
				}
				return api, &api.retry
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			api, policy := tc.build(server.URL)
			*policy = fastPolicy()

			reply, _, err := api.Generate(context.Background(), []contracts.ChatMessage{
				contracts.NewSystemMessage("be brief"),
				contracts.NewUserMessage("hello"),
			}, nil, &contracts.GenerateOptions{Stream: true})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("attempts = %d, want 2", got)
			}
			if reply.Content != nil && *reply.Content != "hi" {
				t.Fatalf("streamed content = %q, want exactly one \"hi\"", *reply.Content)
			}
		})
	}
}

func TestRetryPolicyReturnsPermanentStatusWithoutRetrying(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	policy := fastPolicy()
	if _, err := policy.send(context.Background(), "test", server.Client(), nil, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, server.URL, nil)
	}); err == nil {
		t.Fatal("send succeeded, want a permanent failure")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestRetryPolicyDoesNotRetryBuildFailures(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	api, err := NewOpenAICompatableAPI(contracts.NewProvisionedModel(
		contracts.OpenAIGeneric, "openai-generic", server.URL, contracts.OpenAICompatableAPI,
		contracts.LLMProviderAuth{Method: contracts.APIKey}, textOnlyModel("gpt-4o"),
	))
	if err != nil {
		t.Fatal(err)
	}
	api.retry = fastPolicy()

	_, _, err = api.Generate(context.Background(), []contracts.ChatMessage{contracts.NewUserMessage("hello")}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("generate error = %v, want a missing API key", err)
	}
	if got := attempts.Load(); got != 0 {
		t.Fatalf("attempts = %d, want 0: a configuration failure must not reach the endpoint", got)
	}
}

func TestRetryPolicyStopsOnCancellationDuringBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	policy := retryPolicy{maxAttempts: 5, base: 10 * time.Second, maxDelay: time.Minute}
	_, err := policy.send(ctx, "test", server.Client(), nil, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("send error = %v, want %v", err, context.DeadlineExceeded)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1: the backoff wait must end with the context", got)
	}
}

func TestRetryPolicyStopsWhenCallerCancelsTheRetryNotice(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var notices []string
	opts := &contracts.GenerateOptions{OnDelta: func(kind contracts.EventStreamKind, text string) bool {
		if kind != contracts.EventProviderEvent {
			t.Fatalf("retry notice kind = %q, want %q", kind, contracts.EventProviderEvent)
		}
		notices = append(notices, text)
		return true
	}}

	policy := retryPolicy{maxAttempts: 5, base: 10 * time.Second, maxDelay: time.Minute}
	_, err := policy.send(context.Background(), "test", server.Client(), opts, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, server.URL, nil)
	})
	if err != harnessErr.ErrTurnCanceled {
		t.Fatalf("send error = %v, want %v", err, harnessErr.ErrTurnCanceled)
	}
	if len(notices) != 1 {
		t.Fatalf("retry notices = %v, want one", notices)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1: a cancelled retry must not be attempted", got)
	}
}

// TestCodexDoesNotRetryAfterStreamedDelta pins the rule that matters most for
// streaming providers: once part of a response has reached the caller, a
// failure is final. A retry would deliver the same text twice.
func TestCodexDoesNotRetryAfterStreamedDelta(t *testing.T) {
	const inBandFailure = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n"

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(inBandFailure))
	}))
	defer server.Close()

	api, err := NewOpenAICodexResponsesAPI(codexModel(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	api.retry = fastPolicy()

	var streamed []string
	_, _, err = api.Generate(context.Background(), []contracts.ChatMessage{
		contracts.NewSystemMessage("be brief"),
		contracts.NewUserMessage("hello"),
	}, nil, &contracts.GenerateOptions{Stream: true, OnDelta: func(kind contracts.EventStreamKind, text string) bool {
		streamed = append(streamed, text)
		return false
	}})
	if err == nil {
		t.Fatal("generate succeeded, want the in-band failure")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if len(streamed) != 1 || streamed[0] != "partial" {
		t.Fatalf("streamed deltas = %v, want [partial]", streamed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "empty", value: "", want: 0},
		{name: "seconds", value: "30", want: 30 * time.Second},
		{name: "zero", value: "0", want: 0},
		{name: "garbage", value: "soon", want: 0},
		{name: "http date", value: now.Add(time.Minute).UTC().Format(http.TimeFormat), want: time.Minute},
		{name: "past date", value: now.Add(-time.Minute).UTC().Format(http.TimeFormat), want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.value, now)
			if delta := got - tc.want; delta > time.Second || delta < -time.Second {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestRetryPolicyTransportFailureClassification drives the real retry loop
// against each way a request can fail before headers arrive. The build callback
// counts attempts, so an endpoint that never reaches the handler (bad
// certificate, bad scheme) is still counted exactly.
func TestRetryPolicyTransportFailureClassification(t *testing.T) {
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer redirecting.Close()

	untrustedTLS := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer untrustedTLS.Close()

	sleeping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer sleeping.Close()

	timeoutClient := &http.Client{Timeout: 20 * time.Millisecond}

	tests := []struct {
		name   string
		url    string
		client *http.Client
		want   int32 // attempts the policy should make
	}{
		{name: "connection refused", url: "http://127.0.0.1:1/v1", want: 5},
		{name: "client timeout", url: sleeping.URL, client: timeoutClient, want: 5},
		{name: "unknown host", url: "http://no-such-host.invalid/v1", want: 1},
		{name: "unsupported scheme", url: "foo://bar/v1", want: 1},
		{name: "redirect loop", url: redirecting.URL + "/loop", want: 1},
		{name: "untrusted certificate", url: untrustedTLS.URL, want: 1},
		{name: "https to a plain http server", url: "https://" + redirecting.Listener.Addr().String(), want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.client
			if client == nil {
				client = http.DefaultClient
			}

			var attempts int32
			policy := retryPolicy{maxAttempts: 5, base: time.Millisecond, maxDelay: time.Millisecond}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			_, err := policy.send(ctx, "test", client, nil, func() (*http.Request, error) {
				attempts++
				return http.NewRequestWithContext(ctx, http.MethodPost, tc.url, nil)
			})
			if err == nil {
				t.Fatal("send succeeded, want a transport failure")
			}
			if attempts != tc.want {
				t.Fatalf("attempts = %d, want %d (error: %v)", attempts, tc.want, err)
			}
		})
	}
}
