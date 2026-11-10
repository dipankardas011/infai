package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// scrollbar renders a 1-column-wide vertical scrollbar of the given height.
// position is where the viewport sits in the content (0.0 = top, 1.0 = bottom),
// and visible is the fraction of the content the viewport shows (0.0–1.0).
// When visible >= 1.0 the entire track is drawn as empty — nothing to scroll.
func scrollbar(height int, position, visible float64, track, thumb lipgloss.Style) string {
	if height <= 0 {
		return ""
	}
	if visible >= 1.0 {
		return track.Width(1).Height(height).Render("")
	}

	// Thumb size: at least 1 row, at most the full track.
	thumbSize := max(int(float64(height)*visible+0.5), 1)
	if thumbSize >= height {
		thumbSize = height
	}

	// Thumb position: clamped so it never exceeds the track.
	space := height - thumbSize
	offset := int(position*float64(space) + 0.5)
	if offset < 0 {
		offset = 0
	}
	if offset > space {
		offset = space
	}

	var b strings.Builder
	for i := range height {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i >= offset && i < offset+thumbSize {
			b.WriteString(thumb.Render(" "))
		} else {
			b.WriteString(track.Render(" "))
		}
	}
	return b.String()
}
