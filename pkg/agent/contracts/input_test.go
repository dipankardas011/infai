package contracts_test

import (
	"strings"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func TestValidateUserInputEnforcesModalities(t *testing.T) {
	textOnly := contracts.LLMModelConfiguration{Id: "text-only", Modality: []contracts.LLMSupportedModality{contracts.ModalityText}}
	vision := contracts.LLMModelConfiguration{Id: "vision", Modality: []contracts.LLMSupportedModality{contracts.ModalityText, contracts.ModalityImage}}
	image := contracts.ImageInput{MediaType: "image/png", Data: []byte{1}}

	if err := contracts.ValidateUserInput(vision, contracts.UserInput{Text: "describe", Images: []contracts.ImageInput{image}}); err != nil {
		t.Fatalf("image-capable model rejected text+image: %v", err)
	}
	if err := contracts.ValidateUserInput(vision, contracts.UserInput{Images: []contracts.ImageInput{image}}); err != nil {
		t.Fatalf("image-capable model rejected image-only: %v", err)
	}
	if err := contracts.ValidateUserInput(textOnly, contracts.UserInput{Text: "hello"}); err != nil {
		t.Fatalf("text-only model rejected text: %v", err)
	}

	err := contracts.ValidateUserInput(textOnly, contracts.UserInput{Text: "describe", Images: []contracts.ImageInput{image}})
	if err == nil {
		t.Fatal("text-only model accepted image input")
	}
	message := err.Error()
	if !strings.Contains(message, "text-only") || !strings.Contains(message, "image") {
		t.Fatalf("error does not identify model and modality: %q", message)
	}

	if err := contracts.ValidateUserInput(vision, contracts.UserInput{}); err != nil {
		t.Fatalf("empty input should carry no required modalities: %v", err)
	}
}

func TestValidateModalitiesRejectsUnknownAndDuplicate(t *testing.T) {
	unknown := contracts.LLMModelConfiguration{Id: "x", Modality: []contracts.LLMSupportedModality{"video"}}
	if err := unknown.ValidateModalities(); err == nil {
		t.Fatal("unknown modality was accepted")
	}

	duplicate := contracts.LLMModelConfiguration{Id: "x", Modality: []contracts.LLMSupportedModality{contracts.ModalityText, contracts.ModalityText}}
	if err := duplicate.ValidateModalities(); err == nil {
		t.Fatal("duplicate modality was accepted")
	}

	valid := contracts.LLMModelConfiguration{Id: "x", Modality: []contracts.LLMSupportedModality{contracts.ModalityText, contracts.ModalityImage, contracts.ModalityAudio}}
	if err := valid.ValidateModalities(); err != nil {
		t.Fatalf("valid modalities rejected: %v", err)
	}
	if !valid.SupportsModality(contracts.ModalityImage) || valid.SupportsModality("video") {
		t.Fatal("SupportsModality reported the wrong result")
	}
}

func TestUserInputRequiredModalities(t *testing.T) {
	image := contracts.ImageInput{MediaType: "image/png", Data: []byte{1}}
	if got := (contracts.UserInput{Text: "hi"}).RequiredModalities(); len(got) != 1 || got[0] != contracts.ModalityText {
		t.Fatalf("text modalities=%v", got)
	}
	if got := (contracts.UserInput{Images: []contracts.ImageInput{image}}).RequiredModalities(); len(got) != 1 || got[0] != contracts.ModalityImage {
		t.Fatalf("image modalities=%v", got)
	}
	if got := (contracts.UserInput{Text: "hi", Images: []contracts.ImageInput{image}}).RequiredModalities(); len(got) != 2 {
		t.Fatalf("mixed modalities=%v", got)
	}
	if !(contracts.UserInput{Images: []contracts.ImageInput{image}}).HasImages() {
		t.Fatal("HasImages=false")
	}
	if !(contracts.UserInput{}).Empty() {
		t.Fatal("empty input reported non-empty")
	}
	imageOnly := contracts.UserInput{Images: []contracts.ImageInput{image}}
	if !imageOnly.Empty() {
		t.Fatal("image-only input should carry no text and be treated as empty")
	}
	if !imageOnly.HasImages() {
		t.Fatal("image-only input should still report its images")
	}
}
