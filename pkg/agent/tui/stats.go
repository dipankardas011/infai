package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
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
	tools         []contracts.Tool
	skills        []contracts.Skill
	mcpPrompts    []contracts.MCPPrompt
	mcpResources  []contracts.MCPResource
	mcpStatus     []contracts.MCPServerStatus
	// parent and children are the sessions this one hangs off and owns, drawn
	// with the tree the chat row draws them with. relativeErr is why the engine
	// could not name them, when it could not.
	parent      *statsRelative
	children    []statsRelative
	relativeErr error
}

// statsRelative is another session named by the page: the kind of agent running
// it and the status it reported. A row with no name falls back to the id it was
// found by, and reported is what separates a session the engine answered for
// from one it never did — an answer with no name in it is still an answer.
type statsRelative struct {
	id       uuid.UUID
	name     string
	kind     contracts.AgentKind
	status   contracts.SessionStatus
	reported bool
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

// renderStatsPanel draws the session-stats page. The page is a record rather
// than a menu: what it has to say is longer than a terminal often is, so it is
// scrolled, and nothing it carries is ever cut to fit.
func renderStatsPanel(m *modalModel, width, height int, styles harnessStyles) string {
	contentWidth := max(width-4, 1)
	header := styles.heading(m.title, everforest.Background)
	if m.body != "" {
		header += "\n" + styles.screenBody.Width(contentWidth).Render(m.body)
	}
	header += "\n"
	headerBlock := fullWidth(lipgloss.NewStyle().Padding(1, 2), width, header)

	// The chrome is measured first, so the window is the rows the content will
	// actually be drawn in. Both ends of a scroll are clamped here, which is
	// what makes the drawn window the page's scroll state.
	rows := layoutRows(width, height, intrinsic(headerBlock), fill(), intrinsic(" "))[1].height
	lines := m.stats.lines(contentWidth, styles)
	offset := clamp(m.scroll, 0, max(len(lines)-rows, 0))
	m.scroll, m.rows = offset, rows
	window := lines[offset:min(offset+rows, len(lines))]

	footer := "esc back"
	if len(lines) > rows {
		footer = fmt.Sprintf("%d-%d of %d  ·  ↑/↓ or j/k scroll  ·  esc back", offset+1, offset+len(window), len(lines))
	}
	footerBlock := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, styles.inactive.Render(ansi.Truncate(footer, contentWidth, "…")))
	bodyBlock := fullWidth(lipgloss.NewStyle().Padding(0, 2), width, strings.Join(window, "\n"))

	tracks := layoutRows(width, height, intrinsic(headerBlock), fill(), intrinsic(footerBlock))
	parts := []string{fitArea(tracks[0], headerBlock), fitArea(tracks[1], bodyBlock), fitArea(tracks[2], footerBlock)}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// lines is the whole page, in the order a reader came for it: the window, the
// numbers it holds, then the identity they belong to, and the script the
// session is judged by at the end.
func (s sessionStats) lines(width int, styles harnessStyles) []string {
	var sections [][]string
	for _, section := range [][]string{
		s.identityLines(width, styles),
		s.treeLines(width, styles),
		s.contextLines(width, styles),
		s.lastTurnLines(width, styles),
		s.toolLines(width, styles),
		s.skillLines(width, styles),
		s.mcpLines(width, styles),
		s.scriptLines(width, styles),
	} {
		if len(section) > 0 {
			sections = append(sections, section)
		}
	}
	return strings.Split(statsBlocks(sections...), "\n")
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
	lines := []string{
		statsHeading("SESSION", strings.Join(marks, styles.status.Render("  ·  ")), width, styles),
		statsField("name", name, width, styles),
		statsField("session", s.meta.ID.String(), width, styles),
		statsField("model", model, width, styles),
		statsField("directory", s.meta.Cwd, width, styles),
		statsField("created", humanTime(s.meta.CreatedAt), width, styles),
		statsField("updated", humanTime(s.meta.UpdatedAt), width, styles),
	}
	if s.meta.MaxTurns != nil {
		lines = append(lines, statsField("budget", fmt.Sprintf("%d turns", *s.meta.MaxTurns), width, styles))
	}
	// How a session ended is written once and never rewritten, so it is shown
	// only while the session is still in it, and then only for what the status in
	// the heading cannot say. On a session that has since been reopened it is
	// history, and history that contradicts the heading reads as a bug.
	if ended := s.meta.Conclusion; ended != nil && ended.Status == s.status {
		if reason := singleLine(ended.Reason); reason != "" {
			lines = append(lines, statsField("ended", styles.inactive.Render(reason), width, styles))
		}
	}
	return lines
}

// treeLines draws the sessions this one hangs off and owns, with the arrow and
// the branches the chat row uses for the same relationships.
func (s sessionStats) treeLines(width int, styles harnessStyles) []string {
	if s.parent == nil && len(s.children) == 0 {
		return nil
	}
	lines := []string{statsHeading("TREE", "", width, styles)}
	if s.parent != nil {
		lines = append(lines, statsRelativeRow("↖ ", *s.parent, width, styles))
	}
	for i := range s.children {
		tree := "├─ "
		if i == len(s.children)-1 {
			tree = "└─ "
		}
		lines = append(lines, statsRelativeRow(tree, s.children[i], width, styles))
	}
	// A row left as a bare id is a question the engine never answered, so the
	// page says the call failed instead of leaving the reader to guess that a
	// name exists somewhere.
	if s.relativeErr != nil && s.anyRelativeUnreported() {
		lines = append(lines, statsNote("the engine did not answer: "+singleLine(s.relativeErr.Error()), width, styles))
	}
	return lines
}

// anyRelativeUnreported reports whether the engine left any session of the tree
// unnamed: a live report names its own row, so a row without one is what a
// failed call looks like from here.
func (s sessionStats) anyRelativeUnreported() bool {
	if s.parent != nil && !s.parent.reported {
		return true
	}
	for i := range s.children {
		if !s.children[i].reported {
			return true
		}
	}
	return false
}

// statsRelativeRow is one session of the tree: the branch that places it, the
// kind of agent running it, its name, and the status it reported. A session
// nothing could name is shown by its id in full — that id is the only handle
// there is, and a shortened one cannot be looked up — and it keeps the row's
// plain colour, because an id is a value rather than a title. The status follows
// the name rather than floating at the panel edge: a lone mark half a screen
// away reads as a fact about the session the page is about, not about the row
// it belongs to.
func statsRelativeRow(tree string, relative statsRelative, width int, styles harnessStyles) string {
	prefix := "  " + sessionTree(tree, styles.statusRow.PaddingRight(0))
	kindGlyph, kindStyle := agentKindMark(relative.kind, styles)
	kind := ""
	if kindGlyph != "" {
		kind = kindStyle.Render(kindGlyph) + " "
	}
	name, nameStyle := strings.TrimSpace(relative.name), styles.sessionName
	if name == "" {
		name, nameStyle = relative.id.String(), styles.screenRow
	}
	status := ""
	if relative.reported {
		descriptor := describeSessionStatus(relative.status, styles)
		status = styles.status.Render("  ·  ") + descriptor.style.Render(descriptor.glyph+" "+descriptor.label)
	}
	room := max(width-lipgloss.Width(prefix)-lipgloss.Width(kind)-lipgloss.Width(status), 1)
	return ansi.Truncate(prefix+kind+nameStyle.Render(truncateLine(name, room))+status, width, "…")
}

func statsCapabilityRow(name, description string, width int, styles harnessStyles) string {
	line := "  " + styles.screenRow.Render(singleLine(name))
	if description != "" {
		line += styles.status.Render("  ·  ") + styles.inactive.Render(singleLine(description))
	}
	return ansi.Truncate(line, width, "…")
}

func (s sessionStats) toolLines(width int, styles harnessStyles) []string {
	count := 0
	for _, tool := range s.tools {
		if !contracts.IsMCPTool(tool.Name) {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	lines := []string{statsHeading("TOOLS", styles.status.Render(fmt.Sprintf("%d available", count)), width, styles)}
	for _, tool := range s.tools {
		if contracts.IsMCPTool(tool.Name) {
			continue
		}
		lines = append(lines, statsCapabilityRow(tool.Name, tool.Description, width, styles))
	}
	return lines
}

func (s sessionStats) skillLines(width int, styles harnessStyles) []string {
	if len(s.skills) == 0 {
		return nil
	}
	lines := []string{statsHeading("SKILLS", styles.status.Render(fmt.Sprintf("%d available", len(s.skills))), width, styles)}
	for _, skill := range s.skills {
		lines = append(lines, statsCapabilityRow(skill.Title, skill.Description, width, styles))
	}
	return lines
}

func (s sessionStats) mcpLines(width int, styles harnessStyles) []string {
	toolCount := 0
	for _, tool := range s.tools {
		if contracts.IsMCPTool(tool.Name) {
			toolCount++
		}
	}
	if toolCount == 0 && len(s.mcpPrompts) == 0 && len(s.mcpResources) == 0 && len(s.mcpStatus) == 0 {
		return nil
	}
	tail := fmt.Sprintf("%d tools  ·  %d prompts  ·  %d resources", toolCount, len(s.mcpPrompts), len(s.mcpResources))
	lines := []string{statsHeading("MCP", styles.status.Render(tail), width, styles)}
	for _, tool := range s.tools {
		if contracts.IsMCPTool(tool.Name) {
			lines = append(lines, statsCapabilityRow("tool "+tool.Name, tool.Description, width, styles))
		}
	}
	for _, prompt := range s.mcpPrompts {
		name := prompt.Server + "/" + prompt.Name
		lines = append(lines, statsCapabilityRow("prompt "+name, prompt.Description, width, styles))
	}
	for _, resource := range s.mcpResources {
		name := resource.Server + "  " + resource.URI
		lines = append(lines, statsCapabilityRow("resource "+name, resource.Description, width, styles))
	}
	for _, status := range s.mcpStatus {
		lines = append(lines, statsCapabilityRow("unavailable "+status.Server, status.Reason, width, styles))
	}
	return lines
}

// scriptLines highlights the acceptance script the session was created with:
// the same bash lexer and palette the approval band previews it with, so the
// session is judged by the script the reader can actually read.
func (s sessionStats) scriptLines(width int, styles harnessStyles) []string {
	if s.meta.EvalBashScript == nil {
		return nil
	}
	script := strings.TrimSpace(*s.meta.EvalBashScript)
	if script == "" {
		return nil
	}
	// The script is indented under its heading, so it is highlighted one inset
	// narrower than the page's own content.
	lines := strings.Split(renderApprovalScript(script, max(width-2, 1), styles), "\n")
	out := []string{statsHeading("ACCEPTANCE SCRIPT", styles.status.Render(fmt.Sprintf("%d lines", len(lines))), width, styles)}
	for _, line := range lines {
		out = append(out, "  "+line)
	}
	return out
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
