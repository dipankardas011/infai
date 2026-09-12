// Package vision validates image attachments before they reach a provider.
//
// It is deliberately provider-neutral: it inspects raw bytes, derives the MIME
// type and dimensions, and enforces the harness-wide attachment limits. It
// never trusts clipboard-declared types and never performs I/O.
package vision

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

const (
	// MaxImages is the maximum number of images attached to one user turn.
	MaxImages = 4
	// MaxImageBytes is the maximum encoded size of a single image.
	MaxImageBytes = 5 << 20
	// MaxTotalBytes is the maximum combined encoded size of one turn's images.
	MaxTotalBytes = 16 << 20
	// MaxDimension is the maximum width or height of a single image.
	MaxDimension = 8192
	// MaxPixels guards against decompression bombs whose headers are small.
	MaxPixels = 50_000_000

	mediaTypePNG  = "image/png"
	mediaTypeJPEG = "image/jpeg"
)

// ValidateImage inspects raw bytes and returns a normalized ImageInput. It
// derives the MIME type from the bytes, rejects anything that is not a PNG or
// JPEG, enforces the per-image size and dimension limits, and fully decodes
// the payload so truncated or corrupt images are rejected.
func ValidateImage(data []byte) (contracts.ImageInput, error) {
	if len(data) == 0 {
		return contracts.ImageInput{}, errors.New("image: empty data")
	}
	if len(data) > MaxImageBytes {
		return contracts.ImageInput{}, fmt.Errorf("image: %d bytes exceeds the %d byte per-image limit", len(data), MaxImageBytes)
	}

	mediaType := http.DetectContentType(data)
	if mediaType != mediaTypePNG && mediaType != mediaTypeJPEG {
		return contracts.ImageInput{}, fmt.Errorf("image: unsupported media type %q (only PNG and JPEG are supported)", mediaType)
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return contracts.ImageInput{}, fmt.Errorf("image: corrupt %s data: %w", mediaType, err)
	}
	if (format == "png" && mediaType != mediaTypePNG) || (format == "jpeg" && mediaType != mediaTypeJPEG) || (format != "png" && format != "jpeg") {
		return contracts.ImageInput{}, fmt.Errorf("image: media type %q does not match decoded format %q", mediaType, format)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return contracts.ImageInput{}, errors.New("image: invalid dimensions")
	}
	if config.Width > MaxDimension || config.Height > MaxDimension {
		return contracts.ImageInput{}, fmt.Errorf("image: dimensions %dx%d exceed the %dpx limit", config.Width, config.Height, MaxDimension)
	}
	if config.Width*config.Height > MaxPixels {
		return contracts.ImageInput{}, fmt.Errorf("image: %d pixels exceeds the %d pixel limit", config.Width*config.Height, MaxPixels)
	}

	// Headers alone can parse on a truncated payload; decode the whole image to
	// reject corruption before it reaches a provider.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return contracts.ImageInput{}, fmt.Errorf("image: corrupt %s data: %w", mediaType, err)
	}

	return contracts.ImageInput{
		MediaType: mediaType,
		Data:      data,
		Width:     config.Width,
		Height:    config.Height,
	}, nil
}

// ValidateBatch enforces the attachment count and combined size limits without
// decoding. It is the cheap gate used before per-image inspection.
func ValidateBatch(images []contracts.ImageInput) error {
	if len(images) > MaxImages {
		return fmt.Errorf("image: %d attachments exceed the %d image limit", len(images), MaxImages)
	}
	total := 0
	for i, image := range images {
		if len(image.Data) == 0 {
			return fmt.Errorf("image %d: empty data", i+1)
		}
		total += len(image.Data)
		if total > MaxTotalBytes {
			return fmt.Errorf("image: total %d bytes exceeds the %d byte request limit", total, MaxTotalBytes)
		}
	}
	return nil
}

// ValidateInputs is the authoritative validation used by the engine. It
// re-inspects every supplied image (not trusting client metadata), preserves
// the caller-provided names, and enforces the batch limits.
func ValidateInputs(images []contracts.ImageInput) ([]contracts.ImageInput, error) {
	if err := ValidateBatch(images); err != nil {
		return nil, err
	}
	normalized := make([]contracts.ImageInput, 0, len(images))
	for i, image := range images {
		inspected, err := ValidateImage(image.Data)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", i+1, err)
		}
		if image.Name != "" {
			inspected.Name = image.Name
		}
		normalized = append(normalized, inspected)
	}
	return normalized, nil
}
