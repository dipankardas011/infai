package contracts

import (
	"fmt"
	"slices"
	"strings"
)

// UserInput is the provider-neutral payload for one user turn. Text may be
// empty when the turn carries at least one image; images are raw encoded bytes
// and are translated into provider wire formats only inside adapters.
type UserInput struct {
	Text   string       `json:"text,omitempty"`
	Images []ImageInput `json:"images,omitempty"`
}

// ImageInput is one image attached to a user turn. Data holds the encoded
// PNG or JPEG bytes. MediaType, Width and Height are derived from the bytes
// during validation, never trusted from the clipboard.
type ImageInput struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// HasImages reports whether the input carries any image attachments.
func (in UserInput) HasImages() bool { return len(in.Images) > 0 }

// Empty reports whether the input carries no text. A prompt requires
// non-empty text; images alone do not make a valid turn.
func (in UserInput) Empty() bool { return strings.TrimSpace(in.Text) == "" }

// RequiredModalities returns the distinct input modalities this payload
// supplies, in a stable order.
func (in UserInput) RequiredModalities() []LLMSupportedModality {
	modalities := make([]LLMSupportedModality, 0, 2)
	if strings.TrimSpace(in.Text) != "" {
		modalities = append(modalities, ModalityText)
	}
	if len(in.Images) > 0 {
		modalities = append(modalities, ModalityImage)
	}
	return modalities
}

// ValidateModalities rejects malformed configured modality lists: unknown
// values and duplicates are configuration errors, not silent fallbacks.
func (m LLMModelConfiguration) ValidateModalities() error {
	seen := make(map[LLMSupportedModality]struct{}, len(m.Modality))
	for _, modality := range m.Modality {
		switch modality {
		case ModalityText, ModalityImage, ModalityAudio:
		default:
			return fmt.Errorf("model %q declares unknown modality %q", m.Id, modality)
		}
		if _, ok := seen[modality]; ok {
			return fmt.Errorf("model %q declares duplicate modality %q", m.Id, modality)
		}
		seen[modality] = struct{}{}
	}
	return nil
}

// SupportsModality reports whether the model declares the given modality.
func (m LLMModelConfiguration) SupportsModality(modality LLMSupportedModality) bool {
	return slices.Contains(m.Modality, modality)
}

// ValidateUserInput is the authoritative gate for one user turn: every
// modality the payload supplies must be declared by the selected model, and
// the configured modality list itself must be well formed.
func ValidateUserInput(model LLMModelConfiguration, input UserInput) error {
	if err := model.ValidateModalities(); err != nil {
		return err
	}
	for _, modality := range input.RequiredModalities() {
		if !model.SupportsModality(modality) {
			return fmt.Errorf("model %q does not support %s input", model.Id, modality)
		}
	}
	return nil
}
