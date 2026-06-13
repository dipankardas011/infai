package tui

import "github.com/charmbracelet/lipgloss"

// Area is a rectangular terminal region allocated to a component.
type Area struct {
	W int
	H int
}

func NewArea(w, h int) Area {
	return Area{W: max(w, 0), H: max(h, 0)}
}

func (a Area) ReserveHeight(lines int) Area {
	return Area{W: a.W, H: max(a.H-lines, 0)}
}

func (a Area) Inner(padX, padY int) Area {
	return Area{W: max(a.W-padX*2, 0), H: max(a.H-padY*2, 0)}
}

// ClampHeight is a final safety guard for any component rendered into area.
func ClampHeight(area Area, view string) string {
	return lipgloss.NewStyle().MaxHeight(max(area.H, 1)).Render(view)
}

// ClampBox caps a bordered/boxed component to the allocated area.
func ClampBox(area Area) lipgloss.Style {
	return lipgloss.NewStyle().MaxHeight(max(area.H, 1)).MaxWidth(max(area.W, 1))
}
