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

	"github.com/google/uuid"

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
	const sessionID = "11111111-2222-3333-4444-555555555555"
	args := claudeCLIArgs(model, claudeSessionArg{ID: sessionID})
	joined := strings.Join(args, "|")
	for _, required := range []string{
		"-p", "--tools||", "--strict-mcp-config", `--mcp-config|{"mcpServers":{}}`,
		"--setting-sources||", "--disable-slash-commands", "--session-id|" + sessionID,
		"--model|claude-sonnet-5-5",
		"--input-format|stream-json", "--await-initialize",
		"--output-format|stream-json", "--verbose", "--include-partial-messages", "--effort|low",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "--no-session-persistence") {
		t.Error("the stored conversation is what lets the CLI reuse its prompt cache")
	}
	// One argument at 128 KiB fails exec with E2BIG, so no payload may travel
	// in argv: the prompt and the schema go on stdin.
	for _, payload := range []string{"--system-prompt|", "--json-schema|"} {
		if strings.Contains(joined, payload) {
			t.Errorf("argv carries a payload (%q): %q", payload, joined)
		}
	}
	resumed := strings.Join(claudeCLIArgs(model, claudeSessionArg{ID: sessionID, Resume: true}), "|")
	if !strings.Contains(resumed, "--resume|"+sessionID) || strings.Contains(resumed, "--session-id") {
		t.Errorf("resumed turn args = %q", resumed)
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
	// The system prompt travels in --system-prompt. Repeating it here would
	// put the same bytes in the message prefix that is rewritten every turn.
	if strings.Contains(first, "you are infai") {
		t.Fatalf("transcript repeats the system prompt: %s", first)
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

// The prompt and the schema must ride the initialize control request, not argv:
// one argument at 128 KiB fails exec with E2BIG, and two MCP servers serialize
// a schema past that.
func TestClaudeStdinSendsThePromptAndSchemaOnTheInitializeLine(t *testing.T) {
	const schema = `{"type":"object","properties":{"content":{"type":"string"}}}`
	stdin := claudeStdin(claudeStdinInput{
		SystemPrompt: "you are infai",
		Schema:       schema,
		RequestID:    "req-1",
		SessionID:    "session-1",
		Transcript:   `{"messages":[]}`,
	})
	lines := strings.Split(strings.TrimSuffix(stdin, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdin has %d lines, want initialize + user:\n%s", len(lines), stdin)
	}
	var init struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype      string          `json:"subtype"`
			SystemPrompt string          `json:"systemPrompt"`
			JSONSchema   json.RawMessage `json:"jsonSchema"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatalf("initialize line: %v\n%s", err, lines[0])
	}
	if init.Type != "control_request" || init.Request.Subtype != "initialize" || init.RequestID != "req-1" {
		t.Fatalf("initialize envelope = %+v", init)
	}
	if init.Request.SystemPrompt != "you are infai" {
		t.Fatalf("systemPrompt = %q", init.Request.SystemPrompt)
	}
	if string(init.Request.JSONSchema) != schema {
		t.Fatalf("jsonSchema = %s", init.Request.JSONSchema)
	}
	var user struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &user); err != nil {
		t.Fatalf("user line: %v\n%s", err, lines[1])
	}
	if user.Type != "user" || len(user.Message.Content) != 1 || user.Message.Content[0].Text != `{"messages":[]}` {
		t.Fatalf("user envelope = %+v", user)
	}
	if !strings.HasSuffix(stdin, "\n") {
		t.Error("stream-json input must be newline delimited")
	}
}

func TestClaudeStdinCarriesImagesAsStreamJSONBlocks(t *testing.T) {
	transcript := `{"messages":[{"role":"user","images":[{"index":0,"media_type":"image/png"}]}]}`
	image := contracts.ImageInput{MediaType: "image/png", Data: []byte("png-bytes")}
	stdin := claudeStdin(claudeStdinInput{Transcript: transcript, Images: []contracts.ImageInput{image}})
	lines := strings.Split(strings.TrimSuffix(stdin, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdin has %d lines, want initialize + user:\n%s", len(lines), stdin)
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
	if err := json.Unmarshal([]byte(lines[1]), &envelope); err != nil {
		t.Fatalf("user line is not JSON: %v\n%s", err, lines[1])
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
	usage := claudeUsage(event.Usage, nil)
	if usage == nil {
		t.Fatal("usage = nil")
	}
	// Without iterations the top level is the last request too.
	if usage.PromptTokens != 9 || usage.CompletionTokens != 5 ||
		usage.TotalTokens != 14 || usage.ContextTokens != 14 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.PromptTokensDetails == nil ||
		usage.PromptTokensDetails.CachedTokens != 4 || usage.PromptTokensDetails.CacheWriteTokens != 3 {
		t.Fatalf("details = %+v", usage.PromptTokensDetails)
	}
}

// The CLI sums every request of one -p process into the top level. Only the
// last iteration is the context window the last API request occupied.
func TestClaudeUsageSeparatesSpendFromContext(t *testing.T) {
	event := claudeStreamEvent{}
	event.Usage.InputTokens = 30
	event.Usage.CacheCreationInputTokens = 40
	event.Usage.CacheReadInputTokens = 50
	event.Usage.OutputTokens = 10
	event.Usage.Iterations = []claudeTokenUsage{
		{InputTokens: 1, CacheCreationInputTokens: 2, CacheReadInputTokens: 20, OutputTokens: 3},
		{InputTokens: 4, CacheCreationInputTokens: 5, CacheReadInputTokens: 60, OutputTokens: 7},
	}
	usage := claudeUsage(event.Usage, nil)
	if usage == nil {
		t.Fatal("usage = nil")
	}
	if usage.PromptTokens != 120 || usage.TotalTokens != 130 {
		t.Fatalf("spend = %+v", usage)
	}
	if usage.ContextTokens != 76 {
		t.Fatalf("context = %d, want the last iteration (4+5+60+7)", usage.ContextTokens)
	}
}

// iterations is optional. When the API omits it, the last assistant message's
// own usage is the per-step number; the result total stays the spend.
func TestClaudeUsageFallsBackToTheLastAssistantStep(t *testing.T) {
	event := claudeStreamEvent{}
	event.Usage.InputTokens = 100
	event.Usage.CacheReadInputTokens = 900
	event.Usage.OutputTokens = 50
	step := claudeTokenUsage{
		InputTokens: 5, CacheCreationInputTokens: 10, CacheReadInputTokens: 80, OutputTokens: 2,
	}
	usage := claudeUsage(event.Usage, &step)
	if usage == nil {
		t.Fatal("usage = nil")
	}
	if usage.TotalTokens != 1050 {
		t.Fatalf("spend = %+v", usage)
	}
	if usage.ContextTokens != 97 {
		t.Fatalf("context = %d, want the last step (5+10+80+2)", usage.ContextTokens)
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
	client, err := NewClaudeCLI(catalogModel(t, "definitely-not-installed-claude", "claude-sonnet-5-5", contracts.ThinkingLow), t.TempDir(), uuid.MustParse("11111111-2222-3333-4444-555555555555"))
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

func TestClaudeConversationResumesWithOnlyTheDelta(t *testing.T) {
	const assignedID = "11111111-2222-3333-4444-555555555555"
	client := &claudeCLI{}
	client.conversation.id = assignedID
	const system = "system prompt"
	history := claudeConversation([]contracts.ChatMessage{
		contracts.NewSystemMessage(system),
		contracts.NewUserMessage("first"),
	})

	client.conversation.mu.Lock()
	first := client.planTurn(history, system)
	client.conversation.mu.Unlock()
	if first.resume || first.sessionID != assignedID {
		t.Fatalf("the first fresh turn must use the infai session id: %+v", first)
	}

	reply := contracts.NewAssistantMessage("answer")
	client.conversation.mu.Lock()
	client.recordSuccess("session-id", history, system, reply)
	client.conversation.mu.Unlock()

	next := append(append([]contracts.ChatMessage{}, history...),
		reply,
		contracts.NewUserMessage("second"),
	)
	client.conversation.mu.Lock()
	turn := client.planTurn(next, system)
	client.conversation.mu.Unlock()
	if !turn.resume || turn.sessionID != "session-id" {
		t.Fatalf("turn = %+v", turn)
	}
	// The CLI stored its own copy of the reply, so the delta carries only the
	// tool results or the next user message.
	if strings.Contains(turn.delta.transcript, `"first"`) || strings.Contains(turn.delta.transcript, `"answer"`) {
		t.Fatalf("delta repeats the stored conversation: %s", turn.delta.transcript)
	}
	if !strings.Contains(turn.delta.transcript, `"second"`) {
		t.Fatalf("delta = %s", turn.delta.transcript)
	}
	if !strings.Contains(turn.full.transcript, `"first"`) {
		t.Fatalf("full transcript = %s", turn.full.transcript)
	}

	// A branch switch can put a different assistant reply at the same
	// position. The CLI holds the old one, so this must start fresh.
	branch := append(append([]contracts.ChatMessage{}, history...),
		contracts.NewAssistantMessage("other branch"),
		contracts.NewUserMessage("second"),
	)
	client.conversation.mu.Lock()
	turn = client.planTurn(branch, system)
	client.conversation.mu.Unlock()
	if turn.resume {
		t.Fatal("a different assistant reply at the pending position must start fresh")
	}
}

func TestClaudeConversationStartsFreshWhenTheHistoryDiverges(t *testing.T) {
	client := &claudeCLI{}
	const system = "system prompt"
	history := claudeConversation([]contracts.ChatMessage{contracts.NewUserMessage("first")})

	client.conversation.mu.Lock()
	client.recordSuccess("session-id", history, system, contracts.NewAssistantMessage("answer"))
	client.conversation.mu.Unlock()

	compacted := []contracts.ChatMessage{contracts.NewUserMessage("summary of the conversation")}
	client.conversation.mu.Lock()
	turn := client.planTurn(compacted, system)
	client.conversation.mu.Unlock()
	if turn.resume {
		t.Fatal("a rewritten history must not append to the stored CLI conversation")
	}

	client.conversation.mu.Lock()
	client.recordFailure()
	client.conversation.mu.Unlock()
	client.conversation.mu.Lock()
	turn = client.planTurn(history, system)
	client.conversation.mu.Unlock()
	if turn.resume {
		t.Fatal("a dirty conversation must start fresh")
	}
}

// The result event is cumulative and iterations is optional, so the last
// assistant message is the per-step fallback for the context measurement.
func TestClaudeStreamUsesTheLastAssistantStepForContext(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}],"usage":{"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"output_tokens":1}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":20,"output_tokens":1}}}`,
		`{"type":"result","usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":40},"structured_output":{"content":"answer","tool_calls":[]}}`,
	}, "\n")
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil {
		t.Fatal("usage = nil")
	}
	if result.Usage.TotalTokens != 640 {
		t.Fatalf("spend = %+v", result.Usage)
	}
	if result.Usage.ContextTokens != 24 {
		t.Fatalf("context = %d, want the last assistant step (1+2+20+1)", result.Usage.ContextTokens)
	}
}

func TestClaudeCLIRecognisesAnInUseSession(t *testing.T) {
	if !isClaudeSessionInUse("Error: Session ID 11111111-2222-3333-4444-555555555555 is already in use.") {
		t.Error("an already used session id must start a new generation")
	}
	if isClaudeSessionInUse("some other failure") {
		t.Error("unrelated failures must not be treated as a used session id")
	}
}

func TestClaudeStreamStreamsTheStructuredAnswer(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"StructuredOutput"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"content\": \"hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"lo\", \"tool_calls\": []}"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput","input":{"content":"hello","tool_calls":[]}}]}}`,
		`{"type":"result","subtype":"success","structured_output":{"content":"hello","tool_calls":[]}}`,
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

	if got, want := strings.Join(deltas, "|"), "content:hel|content:lo"; got != want {
		t.Fatalf("deltas=%q want %q", got, want)
	}
	if !result.ContentStreamed || result.StreamedContent != "hello" {
		t.Fatalf("streamed=%v content=%q", result.ContentStreamed, result.StreamedContent)
	}
	if result.Output == nil || result.Output.Content == nil || *result.Output.Content != "hello" {
		t.Fatalf("output=%+v", result.Output)
	}
}

// A native tool's input is JSON too. Only the StructuredOutput block is the
// answer, so its input must not be published as text.
func TestClaudeStreamDoesNotReadANativeToolInputAsTheAnswer(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"edit"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"content\":\"SECRET\"}"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"edit","input":{"path":"main.go"}}]}}`,
	}, "\n")
	var deltas []string
	result, err := decodeClaudeStream(context.Background(), strings.NewReader(stream), &contracts.GenerateOptions{
		Stream: true,
		OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			deltas = append(deltas, string(kind)+":"+text)
			return false
		},
	}, claudeTools{"edit": {Name: "edit"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 0 {
		t.Fatalf("a native tool input was streamed: %v", deltas)
	}
	if result.ContentStreamed {
		t.Fatal("a native tool input was reported as the answer")
	}
	if result.NativeTool == nil || result.NativeTool.Name != "edit" {
		t.Fatalf("native tool = %+v", result.NativeTool)
	}
}

func TestClaudeGenerateStreamsAndDoesNotRepeatTheAnswer(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"StructuredOutput"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"content\": \"hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"lo\", \"tool_calls\": []}"}}}`,
		`{"type":"result","subtype":"success","structured_output":{"content":"hello","tool_calls":[]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat <<'STREAM'\n"+stream+"STREAM\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	client := &claudeCLI{
		model:  catalogModel(t, script, "claude-sonnet-5-5", ""),
		binary: script,
		opts:   claudeCLIOptions{workspace: dir},
	}
	client.conversation.id = uuid.NewString()

	var deltas []string
	reply, _, err := client.Generate(context.Background(), []contracts.ChatMessage{
		contracts.NewUserMessage("say hello"),
	}, nil, &contracts.GenerateOptions{
		Stream: true,
		OnDelta: func(kind contracts.EventStreamKind, text string) bool {
			deltas = append(deltas, string(kind)+":"+text)
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "provider_event:Claude Code is working|content:hel|content:lo"
	if got := strings.Join(deltas, "|"); got != want {
		t.Fatalf("deltas=%q want %q", got, want)
	}
	if reply.Content == nil || *reply.Content != "hello" {
		t.Fatalf("reply=%+v", reply)
	}
}

func TestClaudeStreamedContentIsNotRepeated(t *testing.T) {
	tests := []struct {
		streamed  string
		validated string
		want      string
	}{
		{"", "hello", "hello"},
		{"hello", "hello", ""},
		{"hel", "hello", "lo"},
		{"other", "hello", ""},
		{"", "", ""},
	}
	for _, test := range tests {
		var got string
		emitClaudeStreamedContent(test.streamed, test.validated, func(text string) bool {
			got += text
			return false
		})
		if got != test.want {
			t.Fatalf("streamed=%q validated=%q emitted=%q want %q", test.streamed, test.validated, got, test.want)
		}
	}
}
