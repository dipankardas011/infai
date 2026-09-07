package tui

import (
	"image/color"

	"charm.land/bubbles/v2/textarea"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	agentstyle "github.com/dipankardas011/infai/pkg/agent/style"
)

// Everforest Dark is the harness's only visual theme. Keeping it in code makes
// every component share the same semantic colors without a theme subsystem.
var everforest = agentstyle.Everforest

type harnessStyles struct {
	app         lipgloss.Style
	header      lipgloss.Style
	brand       lipgloss.Style
	headerMeta  lipgloss.Style
	composer    lipgloss.Style
	status      lipgloss.Style
	statusBusy  lipgloss.Style
	sessionName lipgloss.Style
	muted       lipgloss.Style
	userMarker  lipgloss.Style
	assistant   lipgloss.Style
	thinking    lipgloss.Style
	system      lipgloss.Style
	error       lipgloss.Style
	tool        lipgloss.Style
	skill       lipgloss.Style
	modal       lipgloss.Style
	modalTitle  lipgloss.Style
	modalBody   lipgloss.Style
	modalOption lipgloss.Style
	modalActive lipgloss.Style
	screenTitle lipgloss.Style
	screenBody  lipgloss.Style
	screenRow   lipgloss.Style
	screenSel   lipgloss.Style
	active      lipgloss.Style
	inactive    lipgloss.Style
	menu        lipgloss.Style
	menuRow     lipgloss.Style
	menuActive  lipgloss.Style
}

func newHarnessStyles() harnessStyles {
	return harnessStyles{
		app:         lipgloss.NewStyle().Background(everforest.Background).Foreground(everforest.Text),
		header:      lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text).Padding(0, 1),
		brand:       lipgloss.NewStyle().Foreground(everforest.Green).Bold(true),
		headerMeta:  lipgloss.NewStyle().Foreground(everforest.Muted),
		composer:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(everforest.SurfaceAlt).Padding(0, 1),
		status:      lipgloss.NewStyle().Foreground(everforest.Muted),
		statusBusy:  lipgloss.NewStyle().Foreground(everforest.Yellow).Bold(true),
		sessionName: lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true),
		muted:       lipgloss.NewStyle().Foreground(everforest.Muted),
		userMarker:  lipgloss.NewStyle().Foreground(everforest.Blue).Bold(true),
		assistant:   lipgloss.NewStyle().Foreground(everforest.Text),
		thinking:    lipgloss.NewStyle().Foreground(everforest.Muted).Italic(true),
		system:      lipgloss.NewStyle().Foreground(everforest.Purple),
		error:       lipgloss.NewStyle().Foreground(everforest.Red),
		tool:        lipgloss.NewStyle().Foreground(everforest.Muted),
		skill:       lipgloss.NewStyle().Foreground(everforest.Aqua),
		modal:       lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text).Border(lipgloss.RoundedBorder()).BorderForeground(everforest.Green).Padding(1, 2),
		modalTitle:  lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Green).Bold(true),
		modalBody:   lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Muted),
		modalOption: lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text).PaddingLeft(2),
		modalActive: lipgloss.NewStyle().Background(everforest.SurfaceAlt).Foreground(everforest.Yellow).Bold(true).PaddingLeft(1),
		screenTitle: lipgloss.NewStyle().Foreground(everforest.Green).Bold(true),
		screenBody:  lipgloss.NewStyle().Foreground(everforest.Muted),
		screenRow:   lipgloss.NewStyle().Foreground(everforest.Text),
		screenSel:   lipgloss.NewStyle().Background(everforest.SurfaceAlt).Foreground(everforest.Yellow).Bold(true),
		active:      lipgloss.NewStyle().Foreground(everforest.Green).Bold(true),
		inactive:    lipgloss.NewStyle().Foreground(everforest.Muted),
		menu:        lipgloss.NewStyle().Background(everforest.Surface).BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(everforest.Green),
		menuRow:     lipgloss.NewStyle().Background(everforest.Surface).Foreground(everforest.Text),
		menuActive:  lipgloss.NewStyle().Background(everforest.SurfaceAlt).Foreground(everforest.Yellow).Bold(true),
	}
}

func styleTextarea(input *textarea.Model) {
	s := input.Styles()
	s.Focused.Base = lipgloss.NewStyle().Foreground(everforest.Text)
	s.Focused.Text = lipgloss.NewStyle().Foreground(everforest.Text)
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(everforest.Green).Bold(true)
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(everforest.Muted)
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Focused.Selection = lipgloss.NewStyle().Background(everforest.SurfaceAlt)
	s.Blurred = s.Focused
	s.Cursor.Color = everforest.Green
	input.SetStyles(s)
}

func everforestMarkdownStyle() ansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	margin := uint(0)
	text := agentstyle.TextHex
	muted := agentstyle.MutedHex
	green := agentstyle.GreenHex
	aqua := agentstyle.AquaHex
	blue := agentstyle.BlueHex
	purple := agentstyle.PurpleHex
	red := agentstyle.RedHex
	yellow := agentstyle.YellowHex
	surface := agentstyle.SurfaceHex

	cfg.Document.Margin = &margin
	cfg.Document.Color = &text
	cfg.Heading.Color = &green
	cfg.H1.Color = &yellow
	cfg.H1.BackgroundColor = &surface
	cfg.H6.Color = &aqua
	cfg.HorizontalRule.Color = &muted
	cfg.Link.Color = &blue
	cfg.LinkText.Color = &aqua
	cfg.Image.Color = &purple
	cfg.ImageText.Color = &muted
	cfg.Code.Color = &red
	cfg.Code.BackgroundColor = &surface
	cfg.CodeBlock.Color = &text
	cfg.CodeBlock.Margin = &margin
	if cfg.CodeBlock.Chroma != nil {
		cfg.CodeBlock.Chroma.Text.Color = &text
		cfg.CodeBlock.Chroma.Comment.Color = &muted
		cfg.CodeBlock.Chroma.Keyword.Color = &purple
		cfg.CodeBlock.Chroma.KeywordReserved.Color = &purple
		cfg.CodeBlock.Chroma.KeywordType.Color = &yellow
		cfg.CodeBlock.Chroma.Operator.Color = &red
		cfg.CodeBlock.Chroma.Punctuation.Color = &muted
		cfg.CodeBlock.Chroma.Name.Color = &text
		cfg.CodeBlock.Chroma.NameBuiltin.Color = &aqua
		cfg.CodeBlock.Chroma.NameFunction.Color = &green
		cfg.CodeBlock.Chroma.LiteralNumber.Color = &purple
		cfg.CodeBlock.Chroma.LiteralString.Color = &green
		cfg.CodeBlock.Chroma.GenericDeleted.Color = &red
		cfg.CodeBlock.Chroma.GenericInserted.Color = &green
		cfg.CodeBlock.Chroma.Background.BackgroundColor = &surface
	}
	return cfg
}

func themeBackground() color.Color { return everforest.Background }
func themeForeground() color.Color { return everforest.Text }
