package models

import (
	"encoding/base64"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// imageDataURL builds a provider data URL from canonical image bytes. This is
// the boundary where canonical bytes become a provider-specific representation;
// the canonical ChatMessage never stores a data URL.
func imageDataURL(image contracts.ImageInput) string {
	return "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}

// openAITextPart is the Chat Completions text content part.
type openAITextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// openAIImageURL wraps the data URL exactly as Chat Completions expects.
type openAIImageURL struct {
	URL string `json:"url"`
}

// openAIImagePart is the Chat Completions image content part.
type openAIImagePart struct {
	Type     string         `json:"type"`
	ImageURL openAIImageURL `json:"image_url"`
}

// chatCompletionsContent returns the content array for a user message carrying
// images, or nil when the message has none. Text-only messages keep their
// plain string content so existing wire behavior is unchanged.
func chatCompletionsContent(message contracts.ChatMessage) []any {
	if len(message.Images) == 0 {
		return nil
	}
	parts := make([]any, 0, len(message.Images)+1)
	if text := message.Text(); text != "" {
		parts = append(parts, openAITextPart{Type: "text", Text: text})
	}
	for _, image := range message.Images {
		parts = append(parts, openAIImagePart{
			Type:     "image_url",
			ImageURL: openAIImageURL{URL: imageDataURL(image)},
		})
	}
	return parts
}

// codexTextPart is the Responses API input text content part.
type codexTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexImagePart is the Responses API input image content part. Unlike Chat
// Completions, image_url is a bare string on the part.
type codexImagePart struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

// codexContentParts returns the Responses API content array for a message. It
// always emits a text part for text-only messages (preserving prior behavior);
// image-bearing messages emit text only when non-empty, followed by images.
func codexContentParts(message contracts.ChatMessage) []any {
	parts := make([]any, 0, len(message.Images)+1)
	if len(message.Images) == 0 || message.Text() != "" {
		parts = append(parts, codexTextPart{Type: "input_text", Text: message.Text()})
	}
	for _, image := range message.Images {
		parts = append(parts, codexImagePart{Type: "input_image", ImageURL: imageDataURL(image)})
	}
	return parts
}
