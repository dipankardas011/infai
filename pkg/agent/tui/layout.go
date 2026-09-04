package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// rowTrack describes intent instead of coordinates. Intrinsic tracks consume
// their rendered content height and fill tracks share everything left over.
type rowTrack struct {
	content string
	fill    bool
}

type rowArea struct {
	y      int
	width  int
	height int
}

func intrinsic(content string) rowTrack { return rowTrack{content: content} }
func fill() rowTrack                    { return rowTrack{fill: true} }

func layoutRows(width, height int, tracks ...rowTrack) []rowArea {
	areas := make([]rowArea, len(tracks))
	fixed, fills := 0, 0
	for _, track := range tracks {
		if track.fill {
			fills++
			continue
		}
		fixed += intrinsicHeight(track.content)
	}
	remaining := max(height-fixed, 0)
	heights := make([]int, len(tracks))
	for i, track := range tracks {
		if !track.fill {
			heights[i] = intrinsicHeight(track.content)
		}
	}
	for overflow := max(fixed-height, 0); overflow > 0; overflow-- {
		largest := -1
		for i, trackHeight := range heights {
			if trackHeight > 1 && (largest < 0 || trackHeight > heights[largest]) {
				largest = i
			}
		}
		if largest < 0 {
			break
		}
		heights[largest]--
	}
	for overflow := max(sumHeights(heights)-height, 0); overflow > 0; overflow-- {
		largest := -1
		for i, trackHeight := range heights {
			if trackHeight > 0 && (largest < 0 || trackHeight > heights[largest]) {
				largest = i
			}
		}
		if largest < 0 {
			break
		}
		heights[largest]--
	}
	y := 0
	for i, track := range tracks {
		h := heights[i]
		if track.fill && fills > 0 {
			h = remaining / fills
			remaining -= h
			fills--
		}
		areas[i] = rowArea{y: y, width: width, height: h}
		y += h
	}
	return areas
}

func intrinsicHeight(content string) int {
	if content == "" {
		return 0
	}
	return lipgloss.Height(content)
}

func sumHeights(heights []int) int {
	total := 0
	for _, height := range heights {
		total += height
	}
	return total
}

func fitArea(area rowArea, content string) string {
	if area.width <= 0 || area.height <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Width(area.width).Height(area.height).MaxHeight(area.height).Render(content)
}

func centeredLayer(content string, width, height int) *lipgloss.Layer {
	x := max((width-lipgloss.Width(content))/2, 0)
	y := max((height-lipgloss.Height(content))/2, 0)
	return lipgloss.NewLayer(content).X(x).Y(y)
}

func intrinsicTextWidth(values ...string) int {
	widest := 0
	for _, value := range values {
		for line := range strings.SplitSeq(value, "\n") {
			widest = max(widest, lipgloss.Width(line))
		}
	}
	return widest
}

func contentWidth(style lipgloss.Style, available int) int {
	return max(available-style.GetHorizontalFrameSize(), 1)
}

func spread(width int, style lipgloss.Style, left, right string) string {
	gap := max(contentWidth(style, width)-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// visibleRange keeps a selected item visible within a bounded list.
func visibleRange(length, selected, capacity int) (int, int) {
	if length <= 0 || capacity <= 0 {
		return 0, 0
	}
	capacity = min(capacity, length)
	start := clamp(selected-capacity+1, 0, length-capacity)
	return start, start + capacity
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
