// The human-in-the-loop surface: the reserved band that holds a pending
// decision, and the detail the same decision expands into in the transcript.
// Everything a decision needs to be shown, answered and recorded lives here;
// chat.go only decides when to show it.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// handleApprovalKey routes a key to a pending decision. It reports whether it
// consumed the key; while a reason is being typed every other key belongs to the
// composer, and the transcript scroll keys always work so the context stays
// readable.
func (m *chatModel) handleApprovalKey(key string) (tea.Model, tea.Cmd, bool) {
	if m.approval == nil {
		return m, nil, false
	}
	if m.approvalReason {
		switch key {
		case "esc":
			m.approvalReason = false
			m.composer.Reset()
			m.reflow(false)
			return m, nil, true
		case "enter":
			return m, m.resolveApproval(string(contracts.ApprovalDenyWithReason), strings.TrimSpace(m.composer.Value())), true
		}
		return m, nil, false
	}
	switch key {
	case "a":
		return m, m.resolveApproval(string(contracts.ApprovalApprove), ""), true
	case "d":
		return m, m.resolveApproval(string(contracts.ApprovalDeny), ""), true
	case "r":
		m.approvalReason = true
		m.composer.Reset()
		m.refreshInputMark()
		m.reflow(false)
		return m, nil, true
	case "ctrl+g":
		m.approvalShown = !m.approvalShown
		m.refreshTranscript(true)
		m.reflow(false)
		return m, nil, true
	case "pgup", "pgdown", "ctrl+up", "ctrl+down":
		return m, nil, false
	}
	return m, nil, true
}

// resolveApproval answers the pending decision and records what was answered.
func (m *chatModel) resolveApproval(decision, reason string) tea.Cmd {
	approval := m.approval
	if approval == nil {
		return nil
	}
	m.approval = nil
	m.approvalShown = false
	m.approvalReason = false
	m.composer.Reset()
	note := "Approval " + decision
	if reason != "" {
		note += ": " + reason
	}
	m.blocks = append(m.blocks, block{role: "system", text: note})
	m.refreshTranscript(true)
	m.reflow(false)
	return resolveApprovalCmd(m.ctx, m.client, approval, decision, reason)
}

// showApproval records the pending human decision. It is a reserved block in the
// bottom stack rather than a dialog, so the transcript stays readable while the
// decision is being made.
func (m *chatModel) showApproval(approval *Approval) {
	m.approval = approval
	m.approvalShown = false
	m.approvalReason = false
	m.reflow(false)
}

// clearApproval drops a decision that is no longer ours to make: another client
// answered it, the session concluded, or this client just answered it.
func (m *chatModel) clearApproval() {
	if m.approval == nil {
		return
	}
	m.approval = nil
	m.approvalShown = false
	m.approvalReason = false
	m.composer.Reset()
	m.reflow(false)
}

// approvalView is the reviewed content of a pending decision: the metadata body,
// the script or diff, and the structured change rows.
type approvalView struct {
	body   string
	script string
	rows   []diffRow
}

// approvalHeading names the block a decision expands into.
const approvalHeading = "Human In the Loop"

func newApprovalView(approval *Approval) approvalView {
	if approval == nil || approval.ToolCall == nil {
		return approvalView{}
	}
	body, script := formatApprovalToolCall(*approval.ToolCall)
	return approvalView{body: body, script: script, rows: approvalDiffRows(*approval.ToolCall)}
}

// approvalDetailBlock is the expanded decision: the heading, the tool it is
// about, then the reviewed content. It joins the transcript, which owns the
// scrolling, rather than crowding the reserved rows of the band.
func (m *chatModel) approvalDetailBlock(width int) string {
	name, _ := approvalSubject(m.approval)
	lines := append([]string{
		fullWidth(m.styles.hitlTitle, width, approvalHeading),
		bandLine(m.styles.hitl, width, m.styles.hitlMuted.Render("tool_call: ")+m.styles.hitlName.Render(name)),
	}, approvalDetailLines(newApprovalView(m.approval), width, m.styles)...)
	return strings.Join(lines, "\n")
}

// hitlView is the reserved block for a pending decision. The first row names the
// tool and previews what it would do; the second carries the answers, with the
// way to expand the detail right-aligned. It sits between the task checklist and
// the status row, so a blocked turn stays visible without covering anything.
func (m *chatModel) hitlView() string {
	if m.approval == nil {
		return ""
	}
	band := m.styles.hitl
	name, preview := approvalSubject(m.approval)
	head := m.styles.hitlFlag.Render(" ⚑ ") + m.styles.hitlName.Render(name)
	first := head
	if room := m.width - lipgloss.Width(head) - 2; room > 8 {
		first += band.Render("  ") + m.styles.hitlBody.Render(truncateLine(preview, room))
	}

	answers := band.Render("   ") + m.styles.hitlAllow.Render("[A]llow") +
		band.Render("   ") + m.styles.hitlDeny.Render("[D]eny") +
		band.Render("   ") + m.styles.hitlBody.Render("[R]eason")
	second := answers
	if m.approvalReason {
		// The composer is taking the reason, so the answers are not live; the row
		// names the mode instead of offering keys that would type instead.
		second = band.Render("   ") + m.styles.hitlMuted.Render("deny with reason")
	}
	hint := "(expand with ctrl+g)"
	if m.approvalShown {
		hint = "(collapse with ctrl+g)"
	}
	// The hint is the same subdued grey as a muted span: it is an aside, so it
	// stays on the band instead of carving a darker island out of the tint.
	styled := m.styles.hitlMuted.Render(hint)
	if gap := m.width - lipgloss.Width(second) - lipgloss.Width(styled) - 1; gap >= 2 {
		second += band.Render(strings.Repeat(" ", gap)) + styled
	}
	return bandLine(band, m.width, first) + "\n" + bandLine(band, m.width, second)
}

// bandLine fills a band row out to the given width with the band's own
// background. The terminal drops the background at every style boundary, so the
// spaces between two styled spans and the tail of the row are holes in the tint
// unless they carry the band themselves.
func bandLine(band lipgloss.Style, width int, row string) string {
	if fill := width - lipgloss.Width(row); fill > 0 {
		return row + band.Render(strings.Repeat(" ", fill))
	}
	return row
}

// approvalSubject is the decision in two pieces: the tool it is about, and the
// one line preview of what it would do.
func approvalSubject(approval *Approval) (name, preview string) {
	if approval == nil || approval.ToolCall == nil {
		return "TOOL", "tool call"
	}
	call := *approval.ToolCall
	return string(call.Function.Name), singleLine(toolCallPreview(string(call.Function.Name), call.Function.Arguments))
}

// approvalDetailLines renders the reviewed content for the transcript: the
// metadata first, then the script or the structured diff.
func approvalDetailLines(view approvalView, width int, styles harnessStyles) []string {
	var lines []string
	if view.body != "" {
		lines = append(lines, strings.Split(renderApprovalBody(view.body, width, styles), "\n")...)
	}
	if view.script != "" {
		lines = append(lines, strings.Split(renderApprovalScript(view.script, width, styles), "\n")...)
	}
	if len(view.rows) > 0 {
		if len(lines) > 0 {
			// The separator is part of the band too: a bare empty line would
			// leave the terminal's own background showing through the block.
			lines = append(lines, bandLine(styles.hitl, width, ""))
		}
		oldWidth, newWidth := diffGutterWidths(view.rows)
		codeWidth := max(width-(oldWidth+newWidth+4), 1)
		for _, row := range view.rows {
			lines = append(lines, renderDiffRow(row, oldWidth, newWidth, codeWidth, styles)...)
		}
	}
	return lines
}

// approvalDiffRows returns structured diff rows for edit/write tool calls so the
// review pane can render the same GitHub-style diff as the transcript.
func approvalDiffRows(call contracts.ToolCall) []diffRow {
	switch contracts.ToolType(call.Function.Name) {
	case contracts.EditTool:
		if path, oldText, newText, _, ok := decodeEditArgs(call.Function.Arguments); ok {
			return editDiffRows(path, oldText, newText)
		}
	case contracts.WriteTool:
		if path, content, ok := decodeWriteArgs(call.Function.Arguments); ok {
			return writeDiffRows(path, content)
		}
	}
	return nil
}

func formatApprovalToolCall(call contracts.ToolCall) (string, string) {
	switch contracts.ToolType(call.Function.Name) {
	case contracts.ReadTool:
		preview, ok := readToolCallPreview(call.Function.Arguments)
		if !ok {
			return prettyToolArguments(call.Function.Arguments), ""
		}
		return "SOURCE  " + preview, ""

	case contracts.BashTool:
		var args struct {
			Command string `json:"command"`
			Workdir string `json:"workdir"`
			Timeout *int   `json:"timeout"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return prettyToolArguments(call.Function.Arguments), ""
		}
		workdir := args.Workdir
		if workdir == "" {
			workdir = "workspace root"
		}
		metadata := []string{"The following bash script will be executed.", "", "WORKING DIRECTORY  " + workdir}
		if args.Timeout != nil {
			metadata = append(metadata, fmt.Sprintf("TIMEOUT            %d seconds", *args.Timeout))
		}
		metadata = append(metadata, "", "SCRIPT")
		return strings.Join(metadata, "\n"), args.Command

	case contracts.WriteTool:
		path, content, ok := decodeWriteArgs(call.Function.Arguments)
		if !ok {
			return prettyToolArguments(call.Function.Arguments), ""
		}
		lineCount := 0
		if content != "" {
			lineCount = strings.Count(content, "\n") + 1
		}
		body := fmt.Sprintf("TARGET  %s\nEFFECT  Replace complete file contents\nSIZE    %d lines, %d bytes",
			path, lineCount, len([]byte(content)))
		return body, ""

	case contracts.EditTool:
		path, _, _, replaceAll, ok := decodeEditArgs(call.Function.Arguments)
		if !ok {
			return prettyToolArguments(call.Function.Arguments), ""
		}
		mode := "Replace first exact match"
		if replaceAll {
			mode = "Replace every exact match"
		}
		return fmt.Sprintf("TARGET  %s\nMODE    %s", path, mode), ""
	}

	return prettyToolArguments(call.Function.Arguments), ""
}

func (m *chatModel) handleApprovalUpdate(update ApprovalUpdate) {
	if update.Type == "approval_requested" {
		if update.Approval != nil {
			m.showApproval(update.Approval)
		}
		return
	}
	// Resolved or canceled elsewhere: the decision is no longer ours to make.
	m.clearApproval()
}

func resolveApprovalCmd(ctx context.Context, client Client, approval *Approval, decision, reason string) tea.Cmd {
	return func() tea.Msg {
		if approval == nil {
			return approvalResolvedMsg{err: errors.New("approval is unavailable")}
		}
		return approvalResolvedMsg{err: client.ResolveApproval(ctx, *approval, decision, reason)}
	}
}

// renderApprovalBody paints the metadata an approval carries — the target, the
// mode, the working directory, the timeout — on the attention band. "SCRIPT" is
// the one section header the harness emits, and it labels the inset
// renderApprovalScript draws underneath; script text never lands in here.
func renderApprovalBody(body string, width int, styles harnessStyles) string {
	var rendered []string
	for _, line := range strings.Split(body, "\n") {
		style := styles.hitlMuted
		if line == "SCRIPT" {
			style = styles.active.Background(everforest.AttentionBg).Bold(true)
		}
		rendered = append(rendered, strings.Split(style.Width(width).Render(line), "\n")...)
	}
	return strings.Join(rendered, "\n")
}

// renderApprovalScript highlights a script with the bash lexer on the app
// background rather than the attention band: the code uses the one inset a diff
// uses, and the band is for the decision, not the payload. The chroma style
// names that background on every entry, so no token cuts a hole in the inset.
func renderApprovalScript(script string, width int, styles harnessStyles) string {
	lexer := lexers.Get("bash")
	if lexer != nil {
		iterator, err := chroma.Coalesce(lexer).Tokenise(nil, script)
		if err == nil {
			var highlighted bytes.Buffer
			if err := formatters.TTY16m.Format(&highlighted, approvalBashStyle, iterator); err == nil {
				lineStyle := lipgloss.NewStyle().Background(everforest.Background).Width(width)
				lines := strings.Split(strings.TrimSuffix(highlighted.String(), "\n"), "\n")
				for i := range lines {
					lines[i] = lineStyle.Render(lines[i])
				}
				return strings.Join(lines, "\n")
			}
		}
	}
	return styles.modalBody.Background(everforest.Background).Foreground(everforest.Text).Width(width).Render(script)
}

// approvalBashStyle is the chroma style for a script on the code inset. Every entry
// names that background: chroma resets the terminal at each token boundary, so
// a token that named none would cut a hole in the inset.
var approvalBashStyle = chroma.MustNewStyle("infai-approval-bash", chroma.StyleEntries{
	chroma.Background:      "bg:#272e33",
	chroma.Text:            "#d3c6aa bg:#272e33",
	chroma.Comment:         "#859289 bg:#272e33",
	chroma.CommentPreproc:  "#e69875 bg:#272e33",
	chroma.Keyword:         "#d699b6 bg:#272e33",
	chroma.KeywordReserved: "#d699b6 bg:#272e33",
	chroma.Operator:        "#e67e80 bg:#272e33",
	chroma.Punctuation:     "#859289 bg:#272e33",
	chroma.NameBuiltin:     "#83c092 bg:#272e33",
	chroma.NameFunction:    "#a7c080 bg:#272e33",
	chroma.LiteralNumber:   "#d699b6 bg:#272e33",
	chroma.LiteralString:   "#a7c080 bg:#272e33",
})
