package tui

import (
	"image/color"

	"charm.land/bubbles/v2/textarea"
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
)

// Everforest Dark is the harness's only visual theme. Keeping it in code makes
// every component share the same semantic colors without a theme subsystem.
var everforest = struct {
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

	// Diff row backgrounds (Everforest bg_red / bg_green).
	DiffInsertBg color.Color
	DiffDeleteBg color.Color
}{
	Background: lipgloss.Color("#272e33"),
	Surface:    lipgloss.Color("#2e383c"),
	SurfaceAlt: lipgloss.Color("#374145"),
	Text:       lipgloss.Color("#d3c6aa"),
	Muted:      lipgloss.Color("#859289"),
	Red:        lipgloss.Color("#e67e80"),
	Orange:     lipgloss.Color("#e69875"),
	Yellow:     lipgloss.Color("#dbbc7f"),
	Green:      lipgloss.Color("#a7c080"),
	Aqua:       lipgloss.Color("#83c092"),
	Blue:       lipgloss.Color("#7fbbb3"),
	Purple:     lipgloss.Color("#d699b6"),

	DiffInsertBg: lipgloss.Color("#425047"),
	DiffDeleteBg: lipgloss.Color("#514045"),
}

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
	imageBadge  lipgloss.Style
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
		imageBadge:  lipgloss.NewStyle().Background(everforest.Green).Foreground(everforest.Background).Bold(true),
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
	text := "#d3c6aa"
	muted := "#859289"
	green := "#a7c080"
	aqua := "#83c092"
	blue := "#7fbbb3"
	purple := "#d699b6"
	red := "#e67e80"
	orange := "#e69875"
	yellow := "#dbbc7f"
	surface := "#2e383c"
	diffDelete := "#514045"

	cfg.Document.Margin = &margin
	cfg.Document.Color = &text
	cfg.BlockQuote.Color = &muted
	cfg.Paragraph.Color = &text
	cfg.List.Color = &text
	cfg.Heading.Color = &green
	cfg.H1.Color = &yellow
	cfg.H1.BackgroundColor = &surface
	cfg.H2.Color = &green
	cfg.H3.Color = &aqua
	cfg.H4.Color = &blue
	cfg.H5.Color = &purple
	cfg.H6.Color = &aqua
	cfg.Text.Color = &text
	cfg.Strikethrough.Color = &muted
	cfg.Emph.Color = &text
	cfg.Strong.Color = &yellow
	cfg.HorizontalRule.Color = &muted
	cfg.Item.Color = &green
	cfg.Enumeration.Color = &green
	cfg.Task.Color = &green
	cfg.Link.Color = &blue
	cfg.LinkText.Color = &aqua
	cfg.Image.Color = &purple
	cfg.ImageText.Color = &muted
	cfg.Code.Color = &red
	cfg.Code.BackgroundColor = &surface
	cfg.CodeBlock.Color = &text
	cfg.CodeBlock.Margin = &margin
	cfg.Table.Color = &text
	cfg.DefinitionList.Color = &text
	cfg.DefinitionTerm.Color = &yellow
	cfg.DefinitionDescription.Color = &text
	cfg.HTMLBlock.Color = &muted
	cfg.HTMLSpan.Color = &muted
	if cfg.CodeBlock.Chroma != nil {
		chroma := cfg.CodeBlock.Chroma
		chroma.Text.Color = &text
		chroma.Error.Color = &red
		chroma.Error.BackgroundColor = &diffDelete
		chroma.Comment.Color = &muted
		chroma.CommentPreproc.Color = &orange
		chroma.Keyword.Color = &purple
		chroma.KeywordReserved.Color = &red
		chroma.KeywordNamespace.Color = &aqua
		chroma.KeywordType.Color = &yellow
		chroma.Operator.Color = &orange
		chroma.Punctuation.Color = &muted
		chroma.Name.Color = &text
		chroma.NameBuiltin.Color = &aqua
		chroma.NameTag.Color = &red
		chroma.NameAttribute.Color = &yellow
		chroma.NameClass.Color = &yellow
		chroma.NameConstant.Color = &purple
		chroma.NameDecorator.Color = &orange
		chroma.NameException.Color = &red
		chroma.NameFunction.Color = &green
		chroma.NameOther.Color = &text
		chroma.Literal.Color = &orange
		chroma.LiteralNumber.Color = &purple
		chroma.LiteralDate.Color = &aqua
		chroma.LiteralString.Color = &green
		chroma.LiteralStringEscape.Color = &yellow
		chroma.GenericDeleted.Color = &red
		chroma.GenericEmph.Color = &text
		chroma.GenericInserted.Color = &green
		chroma.GenericStrong.Color = &yellow
		chroma.GenericSubheading.Color = &aqua
		chroma.Background.BackgroundColor = &surface
	}
	return cfg
}

func everforestThinkingMarkdownStyle() ansi.StyleConfig {
	cfg := everforestMarkdownStyle()
	muted := "#859289"
	setMarkdownForeground(&cfg, &muted)
	cfg.H1.BackgroundColor = nil
	cfg.CodeBlock.Chroma = nil
	return cfg
}

func setMarkdownForeground(cfg *ansi.StyleConfig, foreground *string) {
	for _, target := range []**string{
		&cfg.Document.Color,
		&cfg.BlockQuote.Color,
		&cfg.Paragraph.Color,
		&cfg.List.Color,
		&cfg.Heading.Color,
		&cfg.H1.Color,
		&cfg.H2.Color,
		&cfg.H3.Color,
		&cfg.H4.Color,
		&cfg.H5.Color,
		&cfg.H6.Color,
		&cfg.Text.Color,
		&cfg.Strikethrough.Color,
		&cfg.Emph.Color,
		&cfg.Strong.Color,
		&cfg.HorizontalRule.Color,
		&cfg.Item.Color,
		&cfg.Enumeration.Color,
		&cfg.Task.Color,
		&cfg.Link.Color,
		&cfg.LinkText.Color,
		&cfg.Image.Color,
		&cfg.ImageText.Color,
		&cfg.Code.Color,
		&cfg.CodeBlock.Color,
		&cfg.Table.Color,
		&cfg.DefinitionList.Color,
		&cfg.DefinitionTerm.Color,
		&cfg.DefinitionDescription.Color,
		&cfg.HTMLBlock.Color,
		&cfg.HTMLSpan.Color,
	} {
		*target = foreground
	}
}

func themeBackground() color.Color { return everforest.Background }
func themeForeground() color.Color { return everforest.Text }
