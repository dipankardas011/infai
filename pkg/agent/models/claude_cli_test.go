package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// The provider is only useful when the user's real CLI is installed and signed
// in, so the CLI-facing test runs the real binary and skips when it is absent.
func realClaude(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	if home, err := os.UserHomeDir(); err == nil {
		local := filepath.Join(home, ".local", "bin")
		t.Setenv("PATH", local+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	return path
}

// catalogModel renders a catalog entry the way the engine does.
func catalogModel(t *testing.T, binary, modelID string, thinking contracts.InfaiThinkingLevel) contracts.ProvisionedModel {
	t.Helper()
	raw, err := os.ReadFile("../../../catalog/v1/providers/claude-cli.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]contracts.LLMModelConfiguration
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog[modelID]
	if !ok {
		t.Fatalf("%s is not in the catalog", modelID)
	}
	provisioned := contracts.NewProvisionedModel(
		contracts.ClaudeCLI, string(contracts.ClaudeCLI), binary,
		contracts.ClaudeCLIPrint, contracts.LLMProviderAuth{Method: contracts.NoneAuth}, spec,
	)
	if thinking != "" {
		provisioned, err = provisioned.WithThinkingPattern(thinking)
		if err != nil {
			t.Fatal(err)
		}
	}
	return provisioned
}

func testClaudeTools() []contracts.Tool {
	return []contracts.Tool{
		{Name: string(contracts.ReadTool), Description: "read a file",
			Parameters: contracts.ToolParameterObjectSchema(map[string]any{"path": map[string]any{"type": "string"}}, []string{"path"})},
	}
}

func TestClaudeCLIArgsKeepToolsDisabledAndPassTheCatalogID(t *testing.T) {
	args := claudeCLIArgs(catalogModel(t, "claude", "claude-sonnet-5-5", contracts.ThinkingLow), "system prompt", `{"type":"object"}`)
	joined := strings.Join(args, "|")
	for _, required := range []string{
		"-p", "--tools||", "--strict-mcp-config", `--mcp-config|{"mcpServers":{}}`,
		"--setting-sources||", "--disable-slash-commands", "--no-session-persistence",
		"--system-prompt|system prompt", "--model|claude-sonnet-5-5", `--json-schema|{"type":"object"}`,
		"--output-format|stream-json", "--verbose", "--include-partial-messages", "--effort|low",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "--bare") {
		t.Error("--bare drops the subscription login and must never be passed")
	}
}

func TestClaudeToolSchemaCarriesEachToolOwnArguments(t *testing.T) {
	schema, allowed, err := claudeToolSchema(testClaudeTools())
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 1 {
		t.Fatalf("allowed = %v", allowed)
	}
	var decoded struct {
		Properties struct {
			Content struct {
				Type []string `json:"type"`
			} `json:"content"`
			ToolCalls struct {
				Items struct {
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
	if fmt.Sprint(decoded.Properties.Content.Type) != "[string null]" {
		t.Errorf("content type = %v", decoded.Properties.Content.Type)
	}
	variant := decoded.Properties.ToolCalls.Items.OneOf[0]
	if variant.Properties.Name["const"] != string(contracts.ReadTool) {
		t.Errorf("variant name = %v", variant.Properties.Name)
	}
	if _, ok := variant.Properties.Arguments["properties"].(map[string]any)["path"]; !ok {
		t.Errorf("arguments schema = %v", variant.Properties.Arguments)
	}
}

func TestClaudeToolSchemaWithoutToolsRefusesThem(t *testing.T) {
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
				MaxItems int `json:"maxItems"`
				Items    any `json:"items"`
			} `json:"tool_calls"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Properties.ToolCalls.MaxItems != 0 || decoded.Properties.ToolCalls.Items != false {
		t.Fatalf("tool_calls = %+v", decoded.Properties.ToolCalls)
	}
}

func TestClaudeTranscriptIsStableAndAppendOnly(t *testing.T) {
	history := []contracts.ChatMessage{
		contracts.NewSystemMessage("you are infai"),
		contracts.NewUserMessage("read main.go"),
	}
	first := claudeTranscriptText(history)
	if second := claudeTranscriptText(history); second != first {
		t.Fatal("the same history serialized differently twice")
	}
	grown := append(append([]contracts.ChatMessage(nil), history...), contracts.NewUserMessage("next"))
	if !strings.HasPrefix(claudeTranscriptText(grown), strings.TrimSuffix(first, "]}")) {
		t.Fatal("appending a turn changed the earlier serialization")
	}
	if system := claudeSystemPrompt(history); !strings.Contains(system, "you are infai") {
		t.Fatalf("system prompt = %s", system)
	}
}

func TestClaudeMessageRefusesAToolItNeverOffered(t *testing.T) {
	result := claudeStreamResult{Output: &claudeStructuredOutput{
		ToolCalls: []claudeStructuredToolCall{{Name: "rm", Arguments: json.RawMessage(`{}`)}},
	}}
	if _, err := claudeMessage(result, map[string]struct{}{"read": {}}); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeUsageCountsCacheAsInput(t *testing.T) {
	var event claudeStreamEvent
	event.Usage.InputTokens = 2
	event.Usage.CacheCreationInputTokens = 3
	event.Usage.CacheReadInputTokens = 4
	event.Usage.OutputTokens = 5
	want := contracts.TokenUsage{PromptTokens: 9, CompletionTokens: 5, TotalTokens: 14}
	if usage := claudeUsage(event); usage == nil || *usage != want {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestClaudeStreamHidesStructuredJSONAndReportsReasoning(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"weighing"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"content\":\"SECRET\"}"}}}`,
		`{"type":"result","structured_output":{"content":"answer","tool_calls":[]}}`,
	}, "\n")
	var deltas []string
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), &contracts.GenerateOptions{
		Stream: true,
		OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			deltas = append(deltas, string(kind)+":"+text)
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(deltas, "|")
	if strings.Contains(joined, "SECRET") || strings.Contains(joined, "partial_json") {
		t.Fatalf("raw structured JSON leaked: %s", joined)
	}
	if joined != "reasoning:weighing" {
		t.Fatalf("deltas = %s", joined)
	}
	if result.Output == nil || result.Output.Content == nil || *result.Output.Content != "answer" {
		t.Fatalf("output = %+v", result.Output)
	}
	if !result.ResultSeen {
		t.Fatal("the result event was not recorded")
	}
}

func TestClaudeCLIPreflightRequiresASignedInCLIOnPath(t *testing.T) {
	methods := claudeCLIAuthMethods()
	if len(methods) != 1 || methods[0].Method != contracts.NoneAuth || methods[0].SecretInput {
		t.Fatalf("methods = %+v", methods)
	}
	if _, err := beginClaudeCLIAuth(context.Background(), contracts.APIKey); err == nil {
		t.Fatal("an API key method was accepted for a provider that must not hold credentials")
	}

	// The installed CLI, signed in: adding the provider succeeds and stores no
	// credential beyond the method.
	realClaude(t)
	flow, err := beginClaudeCLIAuth(context.Background(), contracts.NoneAuth)
	if err != nil {
		t.Fatalf("the signed-in CLI was rejected: %v", err)
	}
	auth, err := flow.Complete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(auth); string(encoded) != `{"method":"none"}` {
		t.Fatalf("adding the provider stored more than the method: %s", encoded)
	}
}

func TestClaudeCLIPreflightRefusesAMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := beginClaudeCLIAuth(context.Background(), contracts.NoneAuth); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestClaudeCLIGenerateAsksForAToolAsStructuredData(t *testing.T) {
	binary := realClaude(t)
	client, err := NewClaudeCLI(catalogModel(t, binary, "claude-sonnet-5-5", contracts.ThinkingLow))
	if err != nil {
		t.Fatal(err)
	}
	message, usage, err := client.Generate(context.Background(), []contracts.ChatMessage{
		{Role: "system", Content: ptr("You are infai's model.")},
		contracts.NewUserMessage(`Answer with content "need the file" and exactly one tool_calls entry: name read, arguments.path main.go. Do not read anything.`),
	}, testClaudeTools(), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v (content %q)", message.ToolCalls, message.Text())
	}
	call := message.ToolCalls[0]
	if call.Function.Name != contracts.ReadTool || call.Function.Arguments != `{"path":"main.go"}` || call.ID == "" || call.Type != "function" {
		t.Fatalf("tool call = %+v", call)
	}
	if usage == nil || usage.PromptTokens == 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestClaudeCLIGenerateAnswersWhenNoToolsAreOffered(t *testing.T) {
	binary := realClaude(t)
	client, err := NewClaudeCLI(catalogModel(t, binary, "claude-haiku-5-5", contracts.ThinkingLow))
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := client.Generate(context.Background(), []contracts.ChatMessage{
		contracts.NewUserMessage("Reply with exactly the word pong and nothing else."),
	}, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(message.ToolCalls) != 0 || !strings.Contains(strings.ToLower(message.Text()), "pong") {
		t.Fatalf("message = %+v", message)
	}
}

func TestClaudeCLIMissingBinaryIsActionable(t *testing.T) {
	client, err := NewClaudeCLI(catalogModel(t, "definitely-not-installed-claude", "claude-sonnet-5-5", contracts.ThinkingLow))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Generate(context.Background(), nil, nil, nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
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
