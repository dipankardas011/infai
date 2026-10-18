package models

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

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
	model := catalogModel(t, "claude", "claude-sonnet-5-5", contracts.ThinkingLow)
	args := claudeCLIArgs(model, "system prompt", `{"type":"object"}`, false)
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
	if strings.Contains(joined, "--input-format") {
		t.Errorf("text-only turns must keep the CLI's default input format: %q", joined)
	}
	if !strings.Contains(strings.Join(claudeCLIArgs(model, "system prompt", `{"type":"object"}`, true), "|"), "--input-format|stream-json") {
		t.Error("image turns must select stream-json input")
	}
	if strings.Contains(joined, "--bare") {
		t.Error("--bare drops the subscription login and must never be passed")
	}
}

func TestClaudeToolSchemaCarriesEachToolOwnInput(t *testing.T) {
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
							Input     map[string]any `json:"input"`
							Arguments map[string]any `json:"arguments"`
						} `json:"properties"`
						Required []string `json:"required"`
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
	if _, ok := variant.Properties.Input["properties"].(map[string]any)["path"]; !ok {
		t.Errorf("input schema = %v", variant.Properties.Input)
	}
	// The envelope must match the CLI's native tool_use block: name plus
	// input. An arguments field here reintroduces the second dialect.
	if fmt.Sprint(variant.Required) != "[name input]" {
		t.Errorf("required = %v", variant.Required)
	}
	if variant.Properties.Arguments != nil {
		t.Error("the schema still carries an arguments property")
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
	system := claudeSystemPrompt(history)
	if !strings.Contains(system, "you are infai") {
		t.Fatalf("system prompt = %s", system)
	}
	// The native channel is disabled, so the model is told last that it must
	// act through the schema; the position carries the instruction.
	if !strings.HasSuffix(system, claudeNativeToolsDisabled) {
		t.Fatalf("system prompt does not end with the native-tool rule: %s", system)
	}
}

func TestClaudeTranscriptPartsIndexImagesInOrder(t *testing.T) {
	history := []contracts.ChatMessage{
		contracts.NewUserMessage("first"),
		contracts.NewUserMessageWithInput(contracts.UserInput{
			Text: "two",
			Images: []contracts.ImageInput{
				{Name: "a.png", MediaType: "image/png", Data: []byte{1}},
				{Name: "b.jpg", MediaType: "image/jpeg", Data: []byte{2}},
			},
		}),
	}
	text, images := claudeTranscriptParts(history)
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
	want := `"images":[{"index":0,"name":"a.png","media_type":"image/png"},{"index":1,"name":"b.jpg","media_type":"image/jpeg"}]`
	if !strings.Contains(text, want) {
		t.Fatalf("transcript = %s", text)
	}
}

func TestClaudeStdinKeepsTextOnlyTurnsOnTheDefaultInputFormat(t *testing.T) {
	transcript := `{"messages":[]}`
	stdin, streamJSON := claudeStdin(transcript, nil)
	if streamJSON {
		t.Fatal("a text-only turn must not select stream-json input")
	}
	if stdin != transcript {
		t.Fatalf("stdin = %q, want the plain transcript", stdin)
	}
}

func TestClaudeStdinCarriesImagesAsStreamJSONBlocks(t *testing.T) {
	transcript := `{"messages":[{"role":"user","images":[{"index":0,"media_type":"image/png"}]}]}`
	image := contracts.ImageInput{MediaType: "image/png", Data: []byte("png-bytes")}
	stdin, streamJSON := claudeStdin(transcript, []contracts.ImageInput{image})
	if !streamJSON {
		t.Fatal("an image turn must select stream-json input")
	}
	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(stdin), &envelope); err != nil {
		t.Fatalf("stdin is not one JSON line: %v\n%s", err, stdin)
	}
	if envelope.Type != "user" || envelope.Message.Role != "user" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if len(envelope.Message.Content) != 2 {
		t.Fatalf("content blocks = %d, want image + text", len(envelope.Message.Content))
	}
	block := envelope.Message.Content[0]
	if block.Type != "image" || block.Source.Type != "base64" || block.Source.MediaType != "image/png" {
		t.Fatalf("image block = %+v", block)
	}
	if block.Source.Data != base64.StdEncoding.EncodeToString(image.Data) {
		t.Fatalf("image data = %q", block.Source.Data)
	}
	if text := envelope.Message.Content[1]; text.Type != "text" || text.Text != transcript {
		t.Fatalf("text block = %+v", text)
	}
	if !strings.HasSuffix(stdin, "\n") {
		t.Error("stream-json input must be newline delimited")
	}
}

// The transcript has to speak the same dialect as the output schema and the
// CLI's native tool_use block; replaying function.arguments as a string is the
// second dialect that made the model nest one inside the other.
func TestClaudeTranscriptUsesTheNativeToolCallShape(t *testing.T) {
	history := []contracts.ChatMessage{{
		Role: "assistant",
		ToolCalls: []contracts.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: contracts.Function{Name: contracts.BashTool, Arguments: `{"command":"pwd"}`},
		}},
	}}
	text := claudeTranscriptText(history)
	want := `{"role":"assistant","tool_calls":[{"id":"call_1","name":"bash","input":{"command":"pwd"}}]}`
	if text != `{"messages":[`+want+`]}` {
		t.Fatalf("transcript = %s", text)
	}
	if strings.Contains(text, `"function"`) || strings.Contains(text, `"arguments"`) {
		t.Fatalf("transcript still carries the OpenAI tool-call shape: %s", text)
	}
}

func TestClaudeStreamHonoursANativeToolUse(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"deciding"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll edit it."},{"type":"tool_use","name":"edit","input":{"path":"main.go","old_string":"a","new_string":"b"}}],"usage":{"input_tokens":7,"output_tokens":3}}}`,
		`{"type":"result","structured_output":{"content":"gave up","tool_calls":[]}}`,
	}, "\n")
	stopped := false
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), nil,
		claudeTools{"edit": {Name: "edit"}}, func() { stopped = true })
	if err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("the turn was not stopped after the native call")
	}
	if result.NativeTool == nil || result.NativeTool.Name != "edit" ||
		string(result.NativeTool.Input) != `{"path":"main.go","old_string":"a","new_string":"b"}` {
		t.Fatalf("native call = %+v", result.NativeTool)
	}
	if result.NativeText != "I'll edit it." {
		t.Fatalf("native text = %q", result.NativeText)
	}
	if result.Reasoning != "deciding" {
		t.Fatalf("reasoning = %q", result.Reasoning)
	}
	if result.Usage == nil || result.Usage.PromptTokens != 7 || result.Usage.CompletionTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}

	message, err := claudeMessage(result, claudeTools{"edit": {Name: "edit"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != contracts.EditTool {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	if got := message.ToolCalls[0].Function.Arguments; got != `{"new_string":"b","old_string":"a","path":"main.go"}` {
		t.Fatalf("arguments = %s", got)
	}
	if message.Text() != "I'll edit it." {
		t.Fatalf("content = %q", message.Text())
	}
}

func TestClaudeStreamIgnoresANativeToolItWasNotOffered(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"rm","input":{}}]}}`,
		`{"type":"result","structured_output":{"content":"answer","tool_calls":[]}}`,
	}, "\n")
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), nil,
		claudeTools{"read": {Name: "read"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeTool != nil {
		t.Fatalf("native call = %+v", result.NativeTool)
	}
	if result.Output == nil || result.Output.Content == nil || *result.Output.Content != "answer" {
		t.Fatalf("output = %+v", result.Output)
	}
}

func TestClaudeMessageRefusesAToolItNeverOffered(t *testing.T) {
	result := claudeStreamResult{Output: &claudeStructuredOutput{
		ToolCalls: []claudeStructuredToolCall{{Name: "rm", Input: json.RawMessage(`{}`)}},
	}}
	if _, err := claudeMessage(result, claudeTools{"read": {Name: "read"}}); err == nil || !strings.Contains(err.Error(), "not offered") {
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
	if usage := claudeUsage(event.Usage); usage == nil || *usage != want {
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
	}, nil, nil)
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
}

func TestClaudeCLIPreflightRefusesAMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := beginClaudeCLIAuth(context.Background(), contracts.NoneAuth); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
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
