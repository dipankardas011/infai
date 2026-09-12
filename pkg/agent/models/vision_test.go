package models

import (
	"encoding/json"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

var testImage = contracts.ImageInput{
	Name:      "clipboard-1.png",
	MediaType: "image/png",
	Data:      []byte("png-bytes"),
	Width:     4,
	Height:    3,
}

const testImageDataURL = "data:image/png;base64,cG5nLWJ5dGVz"

func TestGenericOpenAIWireMessageImageJSON(t *testing.T) {
	messages := openAIWireMessages([]contracts.ChatMessage{
		contracts.NewUserMessageWithInput(contracts.UserInput{Text: "Describe this", Images: []contracts.ImageInput{testImage}}),
	})
	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"` + testImageDataURL + `"}}]}`
	if string(raw) != want {
		t.Fatalf("generic openai request message:\n got: %s\nwant: %s", raw, want)
	}
}

func TestGenericOpenAITextOnlyKeepsStringContent(t *testing.T) {
	messages := openAIWireMessages([]contracts.ChatMessage{contracts.NewUserMessage("hello")})
	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"role":"user","content":"hello"}` {
		t.Fatalf("text-only content changed: %s", raw)
	}
}

func TestDeepSeekWireMessageImageJSON(t *testing.T) {
	messages := deepSeekWireMessages([]contracts.ChatMessage{
		contracts.NewUserMessageWithInput(contracts.UserInput{Text: "Describe this", Images: []contracts.ImageInput{testImage}}),
	})
	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"` + testImageDataURL + `"}}]}`
	if string(raw) != want {
		t.Fatalf("deepseek request message:\n got: %s\nwant: %s", raw, want)
	}
}

func TestCodexInputImageJSON(t *testing.T) {
	instructions, input, err := codexInput([]contracts.ChatMessage{
		contracts.NewUserMessageWithInput(contracts.UserInput{Text: "Describe this", Images: []contracts.ImageInput{testImage}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if instructions != "" {
		t.Fatalf("unexpected instructions: %q", instructions)
	}
	if len(input) != 1 {
		t.Fatalf("input items=%d want 1", len(input))
	}
	raw, err := json.Marshal(input[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "message" || decoded.Role != "user" {
		t.Fatalf("codex item type/role=%q/%q", decoded.Type, decoded.Role)
	}
	wantContent := `[{"type":"input_text","text":"Describe this"},{"type":"input_image","image_url":"` + testImageDataURL + `"}]`
	if string(decoded.Content) != wantContent {
		t.Fatalf("codex content:\n got: %s\nwant: %s", decoded.Content, wantContent)
	}
}

func TestCodexInputImageOnlyOmitsTextPart(t *testing.T) {
	_, input, err := codexInput([]contracts.ChatMessage{
		contracts.NewUserMessageWithInput(contracts.UserInput{Images: []contracts.ImageInput{testImage}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	wantContent := `[{"type":"input_image","image_url":"` + testImageDataURL + `"}]`
	if string(decoded.Content) != wantContent {
		t.Fatalf("codex image-only content:\n got: %s\nwant: %s", decoded.Content, wantContent)
	}
}

func TestChatCompletionsImageOnlyContent(t *testing.T) {
	messages := openAIWireMessages([]contracts.ChatMessage{
		contracts.NewUserMessageWithInput(contracts.UserInput{Images: []contracts.ImageInput{testImage}}),
	})
	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + testImageDataURL + `"}}]}`
	if string(raw) != want {
		t.Fatalf("image-only content:\n got: %s\nwant: %s", raw, want)
	}
}
