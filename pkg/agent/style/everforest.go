// Package style contains the shared visual language for agent interfaces.
package style

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

const (
	BackgroundHex = "#272e33"
	SurfaceHex    = "#2e383c"
	SurfaceAltHex = "#374145"
	TextHex       = "#d3c6aa"
	MutedHex      = "#859289"
	RedHex        = "#e67e80"
	OrangeHex     = "#e69875"
	YellowHex     = "#dbbc7f"
	GreenHex      = "#a7c080"
	AquaHex       = "#83c092"
	BlueHex       = "#7fbbb3"
	PurpleHex     = "#d699b6"
)

// Palette is the Everforest Dark palette used by agent terminal interfaces.
type Palette struct {
	Background color.Color
	Surface    color.Color
	SurfaceAlt color.Color
	Text       color.Color
	Muted      color.Color
	Red        color.Color
	Orange     color.Color
	Yellow     color.Color
	Green      color.Color
	Aqua       color.Color
	Blue       color.Color
	Purple     color.Color
}

var Everforest = Palette{
	Background: lipgloss.Color(BackgroundHex),
	Surface:    lipgloss.Color(SurfaceHex),
	SurfaceAlt: lipgloss.Color(SurfaceAltHex),
	Text:       lipgloss.Color(TextHex),
	Muted:      lipgloss.Color(MutedHex),
	Red:        lipgloss.Color(RedHex),
	Orange:     lipgloss.Color(OrangeHex),
	Yellow:     lipgloss.Color(YellowHex),
	Green:      lipgloss.Color(GreenHex),
	Aqua:       lipgloss.Color(AquaHex),
	Blue:       lipgloss.Color(BlueHex),
	Purple:     lipgloss.Color(PurpleHex),
}
