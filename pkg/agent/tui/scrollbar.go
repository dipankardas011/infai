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

	// Everything is background-based: track and thumb are both a single space
	// with different background colors, arrows sit on the track background.
	arrow := lipgloss.NewStyle().Inherit(track).Inherit(thumb)

	if visible >= 1.0 {
		return track.Width(1).Height(height).Render("")
	}

	// The arrows take the top and bottom rows; the thumb lives in between.
	inner := max(height-2, 1)

	// Thumb size: at least 1 row, at most the full inner track.
	thumbSize := max(int(float64(inner)*visible+0.5), 1)
	if thumbSize >= inner {
		thumbSize = inner
	}

	// Thumb position: clamped so it never exceeds the inner track.
	space := inner - thumbSize
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
		switch {
		case i == 0 && height >= 3:
			b.WriteString(arrow.Render("▴"))
		case i == height-1 && height >= 3:
			b.WriteString(arrow.Render("▾"))
		default:
			ii := i
			if height >= 3 {
				ii = i - 1
			}
			if ii >= offset && ii < offset+thumbSize {
				b.WriteString(thumb.Render(" "))
			} else {
				b.WriteString(track.Render(" "))
			}
		}
	}
	return b.String()
}
