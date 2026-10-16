package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
	harnessErr "github.com/dipankardas011/infai/pkg/agent/errors"
)

// writeFakeClaude installs a stand-in CLI that answers --version and then emits
// the script body, recording argv, cwd and stdin when the environment asks.
func writeFakeClaude(t *testing.T, dir, version, body string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo "%s"; exit 0; fi
[ -n "$ARGV_FILE" ] && { for a in "$@"; do printf '%%s\n' "$a"; done > "$ARGV_FILE"; }
[ -n "$CWD_FILE" ] && pwd > "$CWD_FILE"
if [ -n "$STDIN_FILE" ]; then cat > "$STDIN_FILE"; else cat >/dev/null; fi
%s`, version, body)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testClaudeModel(t *testing.T, binary string) contracts.ProvisionedModel {
	t.Helper()
	effort := "high"
	model := contracts.NewProvisionedModel(
		contracts.ClaudeCLI,
		string(contracts.ClaudeCLI),
		binary,
		contracts.ClaudeCLIPrint,
		contracts.LLMProviderAuth{Method: contracts.NoneAuth},
		contracts.LLMModelConfiguration{
			Id:                "sonnet",
			AvailableThinking: true,
			ThinkingLevels:    contracts.ThinkingLevels{High: &effort},
		},
	)
	model, err := model.WithThinkingPattern(contracts.ThinkingHigh)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func newTestClaudeCLI(t *testing.T, binary string, workDir string) *claudeCLI {
	t.Helper()
	client, err := NewClaudeCLI(testClaudeModel(t, binary))
	if err != nil {
		t.Fatal(err)
	}
	client.opts.workDir = workDir
	client.opts.retryDelay = time.Millisecond
	return client
}

func testClaudeTools() []contracts.Tool {
	return []contracts.Tool{
		{
			Name:        string(contracts.ReadTool),
			Description: "read a file",
			Parameters:  contracts.ToolParameterObjectSchema(map[string]any{"path": map[string]any{"type": "string"}}, []string{"path"}),
		},
		{
			Name:        string(contracts.BashTool),
			Description: "run a command",
			Parameters:  contracts.ToolParameterObjectSchema(map[string]any{"command": map[string]any{"type": "string"}}, []string{"command"}),
		},
	}
}

func TestClaudeCLIArgsKeepToolsDisabled(t *testing.T) {
	args := claudeCLIArgs(testClaudeModel(t, "claude"), "system prompt", `{"type":"object"}`)
	joined := strings.Join(args, "|")
	for _, required := range []string{
		"-p", "--tools||", "--strict-mcp-config", `--mcp-config|{"mcpServers":{}}`,
		"--setting-sources||", "--disable-slash-commands", "--no-session-persistence",
		"--system-prompt|system prompt", "--model|sonnet", `--json-schema|{"type":"object"}`,
		"--output-format|stream-json", "--verbose", "--include-partial-messages", "--effort|high",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "--bare") {
		t.Error("--bare must never be passed: it drops the subscription login")
	}
}

func TestClaudeCLIArgsOmitEffortWhenUnknown(t *testing.T) {
	model := contracts.NewProvisionedModel(
		contracts.ClaudeCLI, string(contracts.ClaudeCLI), "claude", contracts.ClaudeCLIPrint,
		contracts.LLMProviderAuth{Method: contracts.NoneAuth},
		contracts.LLMModelConfiguration{Id: "sonnet"},
	)
	if joined := strings.Join(claudeCLIArgs(model, "s", "{}"), "|"); strings.Contains(joined, "--effort") {
		t.Fatalf("effort passed for a model that declares none: %s", joined)
	}
}

func TestClaudeToolSchemaIsToolSpecificAndSorted(t *testing.T) {
	schema, allowed, err := claudeToolSchema(testClaudeTools())
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 2 {
		t.Fatalf("allowed = %v", allowed)
	}
	var decoded struct {
		Properties struct {
			Content struct {
				Type []string `json:"type"`
			} `json:"content"`
			ToolCalls struct {
				Type     string `json:"type"`
				MaxItems int    `json:"maxItems"`
				Items    struct {
					OneOf []struct {
						Properties struct {
							Name      map[string]any `json:"name"`
							Arguments map[string]any `json:"arguments"`
						} `json:"properties"`
					} `json:"oneOf"`
				} `json:"items"`
			} `json:"tool_calls"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	props := decoded.Properties
	if fmt.Sprint(props.Content.Type) != "[string null]" {
		t.Errorf("content type = %v", props.Content.Type)
	}
	variants := props.ToolCalls.Items.OneOf
	if len(variants) != 2 || props.ToolCalls.MaxItems != 2 {
		t.Fatalf("variants = %d, maxItems = %d", len(variants), props.ToolCalls.MaxItems)
	}
	if variants[0].Properties.Name["const"] != string(contracts.BashTool) {
		t.Errorf("variants are not sorted by name: %v", variants[0].Properties.Name)
	}
	if _, ok := variants[0].Properties.Arguments["properties"].(map[string]any)["command"]; !ok {
		t.Errorf("bash arguments schema = %v", variants[0].Properties.Arguments)
	}
	if _, ok := variants[1].Properties.Arguments["properties"].(map[string]any)["path"]; !ok {
		t.Errorf("read arguments schema = %v", variants[1].Properties.Arguments)
	}
}

func TestClaudeToolSchemaWithoutToolsAllowsNone(t *testing.T) {
	schema, allowed, err := claudeToolSchema(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 0 {
		t.Fatalf("allowed = %v", allowed)
	}
	var decoded struct {
		Properties struct {
			ToolCalls struct {
				Type     string `json:"type"`
				MaxItems int    `json:"maxItems"`
				Items    any    `json:"items"`
			} `json:"tool_calls"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	calls := decoded.Properties.ToolCalls
	if calls.Type != "array" || calls.MaxItems != 0 || calls.Items != false {
		t.Fatalf("tool_calls = %+v", calls)
	}
}

func TestClaudeToolSchemaRejectsDuplicateNames(t *testing.T) {
	_, _, err := claudeToolSchema([]contracts.Tool{{Name: "read"}, {Name: "read"}})
	if err == nil || !strings.Contains(err.Error(), "two tools are named") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeTranscriptIsStableAndAppendOnly(t *testing.T) {
	name := "read"
	history := []contracts.ChatMessage{
		contracts.NewSystemMessage("you are infai"),
		contracts.NewUserMessage("read main.go"),
		{Role: "assistant", Content: ptr("doing it"), ToolCalls: []contracts.ToolCall{{
			ID: "call_1", Type: "function",
			Function: contracts.Function{Name: contracts.ReadTool, Arguments: `{"path":"main.go"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Name: &name, Status: contracts.ToolExecutionSuccess, Content: ptr("package main")},
	}
	first := claudeTranscriptText(history)
	if second := claudeTranscriptText(history); second != first {
		t.Fatal("the same history serialized differently twice")
	}
	grown := append(append([]contracts.ChatMessage(nil), history...), contracts.NewUserMessage("now what"))
	if !strings.HasPrefix(claudeTranscriptText(grown), strings.TrimSuffix(first, "]}")) {
		t.Fatal("appending a turn changed the serialization of the earlier turn")
	}
	system := claudeSystemPrompt(history)
	if !strings.Contains(system, "you are infai") || strings.Contains(system, "read main.go") {
		t.Fatalf("system prompt = %s", system)
	}
}

func TestClaudeMessageRejectsToolsThatWereNotOffered(t *testing.T) {
	content := "hello"
	result := claudeStreamResult{Output: &claudeStructuredOutput{
		Content:   &content,
		ToolCalls: []claudeStructuredToolCall{{Name: "bash", Arguments: json.RawMessage(`{}`)}},
	}}
	if _, err := claudeMessage(result, map[string]struct{}{"read": {}}); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeMessageBuildsToolCallsWithIDs(t *testing.T) {
	result := claudeStreamResult{
		Reasoning: "thinking",
		Output: &claudeStructuredOutput{
			ToolCalls: []claudeStructuredToolCall{{Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`)}},
		},
	}
	message, err := claudeMessage(result, map[string]struct{}{"read": {}})
	if err != nil {
		t.Fatal(err)
	}
	if message.Content != nil || message.ReasoningContent != "thinking" {
		t.Fatalf("message = %+v", message)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	call := message.ToolCalls[0]
	if call.ID == "" || call.Type != "function" || call.Function.Name != contracts.ReadTool || call.Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("tool call = %+v", call)
	}
}

func TestClaudeCLIGenerateOverFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	argvFile, cwdFile, stdinFile := filepath.Join(dir, "argv"), filepath.Join(dir, "cwd"), filepath.Join(dir, "stdin")
	binary := writeFakeClaude(t, dir, "2.1.301", `
cat <<'EOF'
{"type":"result","subtype":"success","structured_output":{"content":"need the file","tool_calls":[{"name":"read","arguments":{"path":"main.go"}}]},"usage":{"input_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":4,"output_tokens":5}}
EOF
`)
	t.Setenv("ARGV_FILE", argvFile)
	t.Setenv("CWD_FILE", cwdFile)
	t.Setenv("STDIN_FILE", stdinFile)
	workDir := t.TempDir()
	client := newTestClaudeCLI(t, binary, workDir)

	var deltas []string
	message, usage, err := client.Generate(context.Background(),
		[]contracts.ChatMessage{contracts.NewUserMessage("read main.go")},
		testClaudeTools(),
		&contracts.GenerateOptions{Stream: true, OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			deltas = append(deltas, string(kind)+":"+text)
			return false
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if message.Text() != "need the file" || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %+v", message)
	}
	if got := message.ToolCalls[0].Function.Arguments; got != `{"path":"main.go"}` {
		t.Fatalf("arguments = %s", got)
	}
	if usage == nil || *usage != (contracts.TokenUsage{PromptTokens: 9, CompletionTokens: 5, TotalTokens: 14}) {
		t.Fatalf("usage = %+v", usage)
	}
	if len(deltas) != 2 || !strings.HasPrefix(deltas[0], "content:need the file") || !strings.HasPrefix(deltas[1], "tool_call:read") {
		t.Fatalf("deltas = %v", deltas)
	}
	if cwd, _ := os.ReadFile(cwdFile); strings.TrimSpace(string(cwd)) != workDir {
		t.Fatalf("the CLI ran in %q, want %q", cwd, workDir)
	}
	if stdin, _ := os.ReadFile(stdinFile); !strings.Contains(string(stdin), "read main.go") {
		t.Fatalf("stdin = %s", stdin)
	}
	argv, _ := os.ReadFile(argvFile)
	if argvText := string(argv); !strings.Contains(argvText, "--json-schema") || !strings.Contains(argvText, `{"mcpServers":{}}`) {
		t.Fatalf("argv = %s", argvText)
	}
}

func TestClaudeCLIStreamingNeverLeaksStructuredJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := writeFakeClaude(t, dir, "2.1.301", `
cat <<'EOF'
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"weighing options"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"content\":\"SECRET\"}"}}}
{"type":"result","subtype":"success","structured_output":{"content":"answer","tool_calls":[]}}
EOF
`)
	client := newTestClaudeCLI(t, binary, t.TempDir())
	var deltas []string
	message, _, err := client.Generate(context.Background(), nil, testClaudeTools(),
		&contracts.GenerateOptions{Stream: true, OnDelta: func(_ contracts.EventStreamKind, text string) bool {
			deltas = append(deltas, text)
			return false
		}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(deltas, "|")
	if strings.Contains(joined, "SECRET") || strings.Contains(joined, "input_json_delta") {
		t.Fatalf("raw structured JSON reached the client: %s", joined)
	}
	if message.Text() != "answer" {
		t.Fatalf("message = %+v", message)
	}
	if joined != "weighing options|answer" {
		t.Fatalf("deltas = %s", joined)
	}
}

func TestClaudeCLIRetriesTransientFailureOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	binary := writeFakeClaude(t, dir, "2.1.301", `
n=$(cat "$COUNT_FILE" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$COUNT_FILE"
if [ "$n" = 1 ]; then
  echo '{"type":"result","subtype":"error_api_error_status","is_error":true,"error":{"api_error_status":503,"message":"overloaded"}}'
else
  echo '{"type":"result","subtype":"success","structured_output":{"content":"recovered","tool_calls":[]}}'
fi
`)
	t.Setenv("COUNT_FILE", count)
	client := newTestClaudeCLI(t, binary, t.TempDir())
	var events []string
	message, _, err := client.Generate(context.Background(), nil, nil,
		&contracts.GenerateOptions{Stream: true, OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			events = append(events, string(kind)+":"+text)
			return false
		}})
	if err != nil {
		t.Fatal(err)
	}
	if message.Text() != "recovered" {
		t.Fatalf("message = %+v", message)
	}
	if len(events) != 2 || !strings.Contains(events[0], "retrying") || events[1] != "content:recovered" {
		t.Fatalf("events = %v", events)
	}
}

func TestClaudeCLIDoesNotRetryAuthOrRateLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	cases := []struct {
		name, event, want string
	}{
		{
			name:  "auth",
			event: `{"type":"result","subtype":"error_auth","is_error":true,"result":"not logged in"}`,
			want:  "isn't signed in",
		},
		{
			name: "rate limit",
			event: `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resets_at":"2030-01-01T12:34:00Z"}}
{"type":"result","subtype":"error_rate_limit","is_error":true}`,
			want: "subscription limit reached",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			count := filepath.Join(dir, "count")
			binary := writeFakeClaude(t, dir, "2.1.301", fmt.Sprintf(`
n=$(cat "$COUNT_FILE" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$COUNT_FILE"
cat <<'EOF'
%s
EOF
`, tc.event))
			t.Setenv("COUNT_FILE", count)
			client := newTestClaudeCLI(t, binary, t.TempDir())
			_, _, err := client.Generate(context.Background(), nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			raw, _ := os.ReadFile(count)
			if strings.TrimSpace(string(raw)) != "1" {
				t.Fatalf("the CLI was invoked %s times, want 1", strings.TrimSpace(string(raw)))
			}
		})
	}
}

func TestClaudeCLIMissingStructuredOutputIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := writeFakeClaude(t, dir, "2.1.301", `echo '{"type":"result","subtype":"success"}'`)
	client := newTestClaudeCLI(t, binary, t.TempDir())
	_, _, err := client.Generate(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no structured output") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeCLIVersionGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := writeFakeClaude(t, dir, "2.1.300", `exit 0`)
	client := newTestClaudeCLI(t, binary, t.TempDir())
	_, _, err := client.Generate(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "2.1.301") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeCLIMissingBinaryIsActionable(t *testing.T) {
	client, err := NewClaudeCLI(testClaudeModel(t, "definitely-not-installed-claude"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Generate(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeCLICancellationStopsTheProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := writeFakeClaude(t, dir, "2.1.301", "sleep 30")
	client := newTestClaudeCLI(t, binary, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := client.Generate(ctx, nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestClaudeCLICanceledTurnReportsTurnCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := writeFakeClaude(t, dir, "2.1.301", `
cat <<'EOF'
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"stopping"}}}
{"type":"result","subtype":"success","structured_output":{"content":"late","tool_calls":[]}}
EOF
`)
	client := newTestClaudeCLI(t, binary, t.TempDir())
	_, _, err := client.Generate(context.Background(), nil, nil,
		&contracts.GenerateOptions{Stream: true, OnDelta: func(contracts.EventStreamKind, string) bool { return true }})
	if !errors.Is(err, harnessErr.ErrTurnCanceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeCLIWorkDirIsInfaiOwnedAndPrivate(t *testing.T) {
	dir, err := claudeCLIWorkDir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("work dir %s has mode %v", dir, info.Mode().Perm())
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cwd, dir) || strings.HasPrefix(dir, cwd) {
		t.Fatalf("work dir %s overlaps the process cwd %s", dir, cwd)
	}
}

func TestClaudeCLIAuthNeedsNoCredential(t *testing.T) {
	methods := claudeCLIAuthMethods()
	if len(methods) != 1 || methods[0].Method != contracts.NoneAuth || methods[0].SecretInput {
		t.Fatalf("methods = %+v", methods)
	}
	flow, err := beginClaudeCLIAuth(contracts.NoneAuth)
	if err != nil {
		t.Fatal(err)
	}
	if challenge := flow.Challenge(); challenge.VerificationURL != "" || challenge.UserCode != "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	auth, err := flow.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth.Method != contracts.NoneAuth || auth.BearerToken != "" || auth.AccessToken != "" || auth.RefreshToken != "" || auth.AccountID != "" || auth.ExpiresAt != nil {
		t.Fatalf("auth carried a credential: %+v", auth)
	}
	if _, err := beginClaudeCLIAuth(contracts.APIKey); err == nil {
		t.Fatal("an API key method was accepted for a provider that must not hold credentials")
	}
}

// The provider must never touch a credential or call the API itself: the CLI
// owns both. This fails the build if that ever changes.
func TestClaudeCLINeverHandlesCredentialsOrCallsTheAPI(t *testing.T) {
	files, err := filepath.Glob("claude_cli*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no claude_cli sources found")
	}
	forbidden := []string{".credentials" + ".json", "CLAUDE_CODE_" + "OAUTH_TOKEN", "api." + "anthropic.com"}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range forbidden {
			if strings.Contains(string(raw), value) {
				t.Fatalf("%s references %q", file, value)
			}
		}
	}
}

func ptr(value string) *string { return &value }
