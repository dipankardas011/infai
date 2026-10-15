package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// sessionStats is the snapshot the session-stats page draws: who the session
// is, how full the model's window is, and how the last turn spent its tokens.
type sessionStats struct {
	meta          store.SessionMeta
	status        contracts.SessionStatus
	contextWindow uint64
	used          uint64
	// usage is the last turn's accounting as this client knows it: the
	// provider's live report when it saw one, otherwise the report the session
	// saved, so a resumed session still shows the turn that filled its context.
	usage         *contracts.TokenUsage
	usageProvider string
	usageModel    string
}

// The stats page's fixed columns: the label column the identity fields and the
// token rows share, the range a token bar may take, and the width of the
// context bar. A bar narrower than the minimum says nothing a number does not,
// so it is dropped rather than drawn.
const (
	statsFieldLabel = 10
	statsMinBar     = 8
	statsMaxBar     = 28
	statsContextBar = 10
)

// renderStatsPanel draws the session-stats page. It is read-only, so a short
// terminal drops whole blocks instead of drawing half of one.
func renderStatsPanel(m *modalModel, width, height int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	header := styles.heading(m.title, everforest.Background)
	if m.body != "" {
		header += "\n" + styles.screenBody.Width(contentWidth).Render(m.body)
	}
	header += "\n"

	headerBlock := fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header)
	footerBlock := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, styles.inactive.Render("esc back"))
	tracks := layoutRows(width, height, intrinsic(headerBlock), fill(), intrinsic(footerBlock))
	bodyBlock := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, m.stats.body(tracks[1].height, contentWidth, styles))

	parts := []string{fitArea(tracks[0], headerBlock), fitArea(tracks[1], bodyBlock), fitArea(tracks[2], footerBlock)}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// body draws the page in the order a reader came for it: the window, the
// numbers it holds, then the identity they belong to. A budget too small for all
// three gives up the identity first, because the session row already names it.
func (s sessionStats) body(budget, width int, styles harnessStyles) string {
	if budget <= 0 {
		return ""
	}
	identity := s.identityLines(width, styles)
	context := s.contextLines(width, styles)
	lastTurn := s.lastTurnLines(width, styles)
	if full := statsBlocks(identity, context, lastTurn); lipgloss.Height(full) <= budget {
		return full
	}
	if numbers := statsBlocks(context, lastTurn); lipgloss.Height(numbers) <= budget {
		return numbers
	}
	return statsBlocks(lastTurn)
}

// statsBlocks joins sections, one blank line apart.
func statsBlocks(sections ...[]string) string {
	joined := make([]string, 0, len(sections))
	for _, section := range sections {
		joined = append(joined, strings.Join(section, "\n"))
	}
	return strings.Join(joined, "\n\n")
}

// statsHeading opens a section: its name, then the facts that belong on its own
// line. The name is the page's quiet grey so the facts keep their colours, and
// the line is cut rather than wrapped: the page measures its blocks before it
// draws them.
func statsHeading(name, tail string, width int, styles harnessStyles) string {
	line := styles.screenBody.Bold(true).Render(name)
	if tail != "" {
		line += "  " + tail
	}
	return ansi.Truncate(line, width, "…")
}

// statsNote is a block's quiet line: an indented sentence that says what the
// numbers above it mean.
func statsNote(text string, width int, styles harnessStyles) string {
	return ansi.Truncate("  "+styles.inactive.Render(text), width, "…")
}

// statsField renders one labelled fact of the identity block.
func statsField(label, value string, width int, styles harnessStyles) string {
	head := "  " + styles.inactive.Width(statsFieldLabel).Render(label) + " "
	if strings.TrimSpace(value) == "" {
		return head + styles.inactive.Render("—")
	}
	return head + styles.screenRow.Render(ansi.Truncate(singleLine(value), max(width-lipgloss.Width(head), 1), "…"))
}

func (s sessionStats) identityLines(width int, styles harnessStyles) []string {
	kindGlyph, kindStyle := agentKindMark(s.meta.AgentKind, styles)
	status := describeSessionStatus(s.status, styles)
	var marks []string
	if kindGlyph != "" {
		marks = append(marks, kindStyle.Render(kindGlyph+" "+agentKindLabel(s.meta.AgentKind)))
	}
	marks = append(marks, status.style.Render(status.glyph+" "+status.label))

	name := s.meta.Name
	if name == "" {
		name = "Untitled session"
	}
	model := orModel(s.meta.Model)
	if s.meta.Provider != "" {
		model += " (" + s.meta.Provider + ")"
	}
	return []string{
		statsHeading("SESSION", strings.Join(marks, styles.status.Render("  ·  ")), width, styles),
		statsField("name", name, width, styles),
		statsField("session", s.meta.ID.String(), width, styles),
		statsField("model", model, width, styles),
		statsField("directory", s.meta.Cwd, width, styles),
		statsField("created", humanTime(s.meta.CreatedAt), width, styles),
		statsField("updated", humanTime(s.meta.UpdatedAt), width, styles),
	}
}

// contextLines answers the question the page exists for: how much of the window
// the conversation has taken, and what "taken" means — the tokens the next
// request will carry, not a count of what has been typed.
func (s sessionStats) contextLines(width int, styles harnessStyles) []string {
	if s.contextWindow == 0 {
		return []string{
			statsHeading("CONTEXT", "", width, styles),
			statsNote("the window of this session's model is not known", width, styles),
		}
	}
	percent := min(int(s.used*100/s.contextWindow), 100)
	head := statsHeading("CONTEXT",
		styles.screenRow.Render(fmt.Sprintf("%s of %s", tokenCount(s.used), tokenCount(s.contextWindow)))+
			"   "+contextProgressBar(styles, percent, statsContextBar)+
			"   "+styles.screenRow.Render(fmt.Sprintf("%d%%", percent)), width, styles)
	note := "the conversation the next request will carry"
	if s.used == 0 {
		note = "nothing measured yet — the next turn reports the size"
	}
	return []string{head, statsNote(note, width, styles)}
}

// lastTurnLines shows where the last turn's tokens went. Its two bars are drawn
// against the same turn, so their lengths compare: a part is filled in the
// colour of the way those tokens were spent, and the cells no part of the line
// covers are the rest of the turn, left dim.
func (s sessionStats) lastTurnLines(width int, styles harnessStyles) []string {
	if s.usage == nil {
		return []string{
			statsHeading("LAST TURN", "", width, styles),
			statsNote("no turn has reported tokens in this session yet", width, styles),
		}
	}

	turn := s.usage.PromptTokens + s.usage.CompletionTokens
	prompt, fresh := s.usage.PromptTokens, s.usage.UncachedPromptTokens()
	cached := prompt - fresh
	promptParts := []statsBarPart{{tokens: prompt, style: styles.freshTokens}}
	promptDetail := ""
	if cached > 0 {
		promptParts = []statsBarPart{{tokens: cached, style: styles.cachedTokens}, {tokens: fresh, style: styles.freshTokens}}
		promptDetail = fmt.Sprintf("cached %s (%d%%)  ·  new %s", tokenCount(cached), cached*100/prompt, tokenCount(fresh))
	}

	completion, reasoning := s.usage.CompletionTokens, uint64(0)
	if details := s.usage.CompletionTokensDetails; details != nil && details.ReasoningTokens <= completion {
		reasoning = details.ReasoningTokens
	}
	completionParts := []statsBarPart{{tokens: completion, style: styles.freshTokens}}
	completionDetail := ""
	if reasoning > 0 {
		completionParts = []statsBarPart{{tokens: reasoning, style: styles.reasoningTokens}, {tokens: completion - reasoning, style: styles.freshTokens}}
		completionDetail = fmt.Sprintf("reasoning %s (%d%%)  ·  text %s", tokenCount(reasoning), reasoning*100/completion, tokenCount(completion-reasoning))
	}

	tail := styles.screenRow.Render(fmt.Sprintf("%s tokens", tokenCount(turn)))
	if s.usageModel != "" {
		produced := s.usageModel
		if s.usageProvider != "" {
			produced += " (" + s.usageProvider + ")"
		}
		tail += styles.status.Render("  ·  ") + styles.muted.Render(produced)
	}
	return []string{
		statsHeading("LAST TURN", tail, width, styles),
		statsTokenRow("prompt", prompt, promptParts, promptDetail, turn, width, styles),
		statsTokenRow("completion", completion, completionParts, completionDetail, turn, width, styles),
	}
}

// statsTokenRow lays out one line of the accounting: its label, its count, the
// bar that splits it, and the breakdown that names the parts. The count is the
// number to read and the bar is only the shape of it, so the breakdown gives up
// its room first and the bar goes second.
func statsTokenRow(label string, tokens uint64, parts []statsBarPart, breakdown string, turn uint64, width int, styles harnessStyles) string {
	head := "  " + styles.inactive.Width(statsFieldLabel).Render(label) +
		styles.screenRow.Width(6).Align(lipgloss.Right).Render(tokenCount(tokens)) + "  "
	room := width - lipgloss.Width(head)
	barWidth := min(max(room-lipgloss.Width(breakdown)-2, 0), statsMaxBar)
	if barWidth < statsMinBar {
		breakdown, barWidth = "", min(max(room, 0), statsMaxBar)
	}
	row := head
	if barWidth >= statsMinBar {
		row += statsBar(parts, turn, barWidth, styles)
		if breakdown != "" {
			row += "  " + breakdown
		}
	}
	return ansi.Truncate(row, width, "…")
}

// statsBarPart is one component of a token count on a bar.
type statsBarPart struct {
	tokens uint64
	style  lipgloss.Style
}

// statsBar draws a line of a turn's tokens against the turn it belongs to. A
// cell belongs to the part that covers the middle of it, and cells no part
// reaches are the rest of the turn, so every bar on the page is measured on one
// scale.
func statsBar(parts []statsBarPart, turn uint64, width int, styles harnessStyles) string {
	// ends[i] is where part i stops, in tokens.
	ends := make([]uint64, len(parts))
	covered := uint64(0)
	for i, part := range parts {
		covered += part.tokens
		ends[i] = covered
	}
	if turn == 0 || width <= 0 || covered == 0 {
		return styles.tokenRest.Render(strings.Repeat("░", max(width, 0)))
	}
	partAt := func(cell int) int {
		position := (2*uint64(cell) + 1) * turn / (2 * uint64(width))
		for i, end := range ends {
			if position < end {
				return i
			}
		}
		return -1 // the rest of the turn
	}
	render := func(part, cells int) string {
		if part < 0 {
			return styles.tokenRest.Render(strings.Repeat("░", cells))
		}
		return parts[part].style.Render(strings.Repeat("█", cells))
	}

	var bar strings.Builder
	start, current := 0, partAt(0)
	for cell := 1; cell < width; cell++ {
		next := partAt(cell)
		if next == current {
			continue
		}
		bar.WriteString(render(current, cell-start))
		start, current = cell, next
	}
	bar.WriteString(render(current, width-start))
	return bar.String()
}
