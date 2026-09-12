package contracts

import (
	"strings"
	"testing"
)

func TestValidateUserInputEnforcesModalities(t *testing.T) {
	textOnly := LLMModelConfiguration{Id: "text-only", Modality: []LLMSupportedModality{ModalityText}}
	vision := LLMModelConfiguration{Id: "vision", Modality: []LLMSupportedModality{ModalityText, ModalityImage}}
	image := ImageInput{MediaType: "image/png", Data: []byte{1}}

	if err := ValidateUserInput(vision, UserInput{Text: "describe", Images: []ImageInput{image}}); err != nil {
		t.Fatalf("image-capable model rejected text+image: %v", err)
	}
	if err := ValidateUserInput(vision, UserInput{Images: []ImageInput{image}}); err != nil {
		t.Fatalf("image-capable model rejected image-only: %v", err)
	}
	if err := ValidateUserInput(textOnly, UserInput{Text: "hello"}); err != nil {
		t.Fatalf("text-only model rejected text: %v", err)
	}

	err := ValidateUserInput(textOnly, UserInput{Text: "describe", Images: []ImageInput{image}})
	if err == nil {
		t.Fatal("text-only model accepted image input")
	}
	message := err.Error()
	if !strings.Contains(message, "text-only") || !strings.Contains(message, "image") {
		t.Fatalf("error does not identify model and modality: %q", message)
	}

	if err := ValidateUserInput(vision, UserInput{}); err != nil {
		t.Fatalf("empty input should carry no required modalities: %v", err)
	}
}

func TestValidateModalitiesRejectsUnknownAndDuplicate(t *testing.T) {
	unknown := LLMModelConfiguration{Id: "x", Modality: []LLMSupportedModality{"video"}}
	if err := unknown.ValidateModalities(); err == nil {
		t.Fatal("unknown modality was accepted")
	}

	duplicate := LLMModelConfiguration{Id: "x", Modality: []LLMSupportedModality{ModalityText, ModalityText}}
	if err := duplicate.ValidateModalities(); err == nil {
		t.Fatal("duplicate modality was accepted")
	}

	valid := LLMModelConfiguration{Id: "x", Modality: []LLMSupportedModality{ModalityText, ModalityImage, ModalityAudio}}
	if err := valid.ValidateModalities(); err != nil {
		t.Fatalf("valid modalities rejected: %v", err)
	}
	if !valid.SupportsModality(ModalityImage) || valid.SupportsModality("video") {
		t.Fatal("SupportsModality reported the wrong result")
	}
}

func TestUserInputRequiredModalities(t *testing.T) {
	image := ImageInput{MediaType: "image/png", Data: []byte{1}}
	if got := (UserInput{Text: "hi"}).RequiredModalities(); len(got) != 1 || got[0] != ModalityText {
		t.Fatalf("text modalities=%v", got)
	}
	if got := (UserInput{Images: []ImageInput{image}}).RequiredModalities(); len(got) != 1 || got[0] != ModalityImage {
		t.Fatalf("image modalities=%v", got)
	}
	if got := (UserInput{Text: "hi", Images: []ImageInput{image}}).RequiredModalities(); len(got) != 2 {
		t.Fatalf("mixed modalities=%v", got)
	}
	if !(UserInput{Images: []ImageInput{image}}).HasImages() {
		t.Fatal("HasImages=false")
	}
	if !(UserInput{}).Empty() {
		t.Fatal("empty input reported non-empty")
	}
	imageOnly := UserInput{Images: []ImageInput{image}}
	if !imageOnly.Empty() {
		t.Fatal("image-only input should carry no text and be treated as empty")
	}
	if !imageOnly.HasImages() {
		t.Fatal("image-only input should still report its images")
	}
}
