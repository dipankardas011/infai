package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestValidateImageDetectsPNGAndJPEG(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		mediaType string
	}{
		{name: "png", data: encodePNG(t, 12, 7), mediaType: "image/png"},
		{name: "jpeg", data: encodeJPEG(t, 9, 5), mediaType: "image/jpeg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateImage(tt.data)
			if err != nil {
				t.Fatalf("ValidateImage: %v", err)
			}
			if got.MediaType != tt.mediaType {
				t.Fatalf("media type=%q want=%q", got.MediaType, tt.mediaType)
			}
			if got.Width <= 0 || got.Height <= 0 {
				t.Fatalf("dimensions=%dx%d want positive", got.Width, got.Height)
			}
			if !bytes.Equal(got.Data, tt.data) {
				t.Fatal("image bytes changed")
			}
		})
	}
}

func TestValidateImageRejectsNonImage(t *testing.T) {
	if _, err := ValidateImage([]byte("this is not an image")); err == nil {
		t.Fatal("non-image bytes were accepted")
	}
	if _, err := ValidateImage(nil); err == nil {
		t.Fatal("empty data was accepted")
	}
}

func TestValidateImageRejectsCorruptImage(t *testing.T) {
	valid := encodePNG(t, 20, 20)
	truncated := valid[:len(valid)/2]
	if _, err := ValidateImage(truncated); err == nil {
		t.Fatal("truncated PNG was accepted")
	}

	corrupt := append([]byte(nil), valid...)
	copy(corrupt[40:], []byte("garbage-garbage"))
	if _, err := ValidateImage(corrupt); err == nil {
		t.Fatal("corrupt PNG was accepted")
	}
}

func TestValidateImageRejectsOversize(t *testing.T) {
	oversize := make([]byte, MaxImageBytes+1)
	if _, err := ValidateImage(oversize); err == nil {
		t.Fatal("oversize image was accepted")
	}
}

func TestValidateImageRejectsLargeDimensions(t *testing.T) {
	wide := encodePNG(t, MaxDimension+1, 1)
	if _, err := ValidateImage(wide); err == nil {
		t.Fatal("image exceeding the dimension limit was accepted")
	}
}

func TestValidateBatchEnforcesCountAndTotal(t *testing.T) {
	tooMany := make([]contracts.ImageInput, MaxImages+1)
	for i := range tooMany {
		tooMany[i] = contracts.ImageInput{MediaType: "image/png", Data: []byte{1}}
	}
	if err := ValidateBatch(tooMany); err == nil {
		t.Fatal("too many images were accepted")
	}

	perImage := MaxTotalBytes/MaxImages + 1
	total := make([]contracts.ImageInput, MaxImages)
	for i := range total {
		total[i] = contracts.ImageInput{MediaType: "image/png", Data: make([]byte, perImage)}
	}
	if err := ValidateBatch(total); err == nil {
		t.Fatal("images exceeding the total limit were accepted")
	}

	if err := ValidateBatch([]contracts.ImageInput{{MediaType: "image/png"}}); err == nil {
		t.Fatal("empty image data was accepted")
	}
}

func TestValidateInputsPreservesNameAndReinspectors(t *testing.T) {
	png := encodePNG(t, 4, 4)
	inputs := []contracts.ImageInput{{Name: "pasted.png", MediaType: "text/plain", Data: png}}
	normalized, err := ValidateInputs(inputs)
	if err != nil {
		t.Fatalf("ValidateInputs: %v", err)
	}
	if normalized[0].Name != "pasted.png" {
		t.Fatalf("name=%q want pasted.png", normalized[0].Name)
	}
	if normalized[0].MediaType != "image/png" {
		t.Fatalf("media type=%q want image/png (bytes must win)", normalized[0].MediaType)
	}
	if normalized[0].Width != 4 || normalized[0].Height != 4 {
		t.Fatalf("dimensions=%dx%d want 4x4", normalized[0].Width, normalized[0].Height)
	}
}
