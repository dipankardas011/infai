package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// selectionPoint is a caret in the transcript: a block, the rendered line inside
// it, and the display column on that line. It is measured on the rendered block
// — what the viewport shows — because that is the only thing a pointer can name.
// A position is stable across scrolling and reflow, so a selection survives both.
type selectionPoint struct {
	block int
	line  int
	col   int
}

func (p selectionPoint) before(other selectionPoint) bool {
	switch {
	case p.block != other.block:
		return p.block < other.block
	case p.line != other.line:
		return p.line < other.line
	default:
		return p.col < other.col
	}
}

// selection is a drag over the transcript: where the button went down, and where
// the pointer has been since.
type selection struct {
	anchor selectionPoint
	cursor selectionPoint
}

func (s selection) ordered() (selectionPoint, selectionPoint) {
	if s.cursor.before(s.anchor) {
		return s.cursor, s.anchor
	}
	return s.anchor, s.cursor
}

// empty reports whether the drag has covered nothing yet, which is what a plain
// click leaves behind.
func (s selection) empty() bool { return s.anchor == s.cursor }

// span is the half-open column range the selection covers on one rendered line.
// indent is the column the block's own text starts at on that line; a selection
// that reaches into the harness's alignment stops there, so neither the band nor
// the copy ever takes the padding the renderer added.
//
// A line the selection does not reach reports false; a line inside it that has
// no columns of its own — a blank line between paragraphs — reports true with an
// empty range, so the copy keeps the blank.
func (s selection) span(block, line, width, indent int) (start, end int, ok bool) {
	from, to := s.ordered()
	if block < from.block || block > to.block {
		return 0, 0, false
	}
	if block == from.block && line < from.line {
		return 0, 0, false
	}
	if block == to.block && line > to.line {
		return 0, 0, false
	}
	first := block == from.block && line == from.line
	last := block == to.block && line == to.line
	switch {
	case first && last:
		start, end = min(from.col, width), min(to.col, width)
	case first:
		start, end = min(from.col, width), width
	case last:
		start, end = 0, min(to.col, width)
	default:
		start, end = 0, width
	}
	if start > end {
		start = end
	}
	if start < indent {
		// The line's own text starts at the indent; a line that ends before it
		// has nothing of its own and collapses to an empty range.
		start = min(indent, end)
	}
	return start, end, true
}

// entryAt finds the block line at a window row. A row on the blank separator
// between two blocks belongs to neither, so it snaps to the block below when
// there is one: a drag that lands between blocks still names a position.
func (w transcriptWindow) entryAt(row int) (windowEntry, int, bool) {
	for _, entry := range w.entries {
		if row >= entry.start && row < entry.start+entry.lines {
			return entry, row - entry.start, true
		}
	}
	for _, entry := range w.entries {
		if row < entry.start {
			return entry, 0, true
		}
	}
	if count := len(w.entries); count > 0 {
		last := w.entries[count-1]
		return last, last.lines - 1, true
	}
	return windowEntry{}, 0, false
}

// handleMouse routes a mouse event over the transcript: the wheel scrolls, a
// click on an artifact chip saves it, and a left drag selects text and copies it
// on release. Selection is the harness's own, so it does not need the terminal
// to be talked out of its mouse capture with a modifier key.
func (m *chatModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if len(m.areas) <= 1 {
		return m, nil
	}
	mouse := msg.Mouse()
	area := m.areas[1]
	inTranscript := mouse.Y >= area.y && mouse.Y < area.y+area.height

	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		if !inTranscript {
			return m, nil
		}
		step := 0
		switch msg.Button {
		case tea.MouseWheelUp:
			step = -transcriptScrollStep
		case tea.MouseWheelDown:
			step = transcriptScrollStep
		}
		// A step that carries the selection moves the view without redrawing it
		// first: the cursor is re-derived from the window that arrives, so one
		// redraw serves the scroll and the selection both.
		if !m.selecting {
			m.scrollTranscript(step)
			return m, nil
		}
		m.dragX, m.dragY = mouse.X, mouse.Y
		m.scrollTranscript(step)
		// A wheel during a drag carries the selection with it: the pointer has
		// not moved, so whatever it now points at is what the drag has reached.
		m.extendSelection()
		return m, m.startDragScroll()
	case tea.MouseClickMsg:
		// A press outside the transcript cannot start a selection: the hit test
		// clamps into the panel, so a press on the header, the composer or the
		// status row would otherwise anchor on whatever line sits under it.
		if msg.Button != tea.MouseLeft || !inTranscript {
			return m, nil
		}
		if hit, found := m.badgeAt(mouse.X, mouse.Y); found {
			return m, m.saveArtifactsCmd(hit)
		}
		point, ok := m.selectionPointAt(mouse.X, mouse.Y)
		if !ok {
			m.clearSelection()
			return m, nil
		}
		m.selection = &selection{anchor: point, cursor: point}
		m.selecting = true
		m.dragX, m.dragY = mouse.X, mouse.Y
		m.syncTranscript()
		return m, m.startDragScroll()
	case tea.MouseMotionMsg:
		if !m.selecting || msg.Button != tea.MouseLeft {
			return m, nil
		}
		m.dragX, m.dragY = mouse.X, mouse.Y
		m.extendSelection()
		return m, m.startDragScroll()
	case tea.MouseReleaseMsg:
		if !m.selecting {
			return m, nil
		}
		m.selecting, m.dragScrolling = false, false
		if point, ok := m.selectionPointAt(mouse.X, mouse.Y); ok {
			m.selection.cursor = point
		}
		if m.selection.empty() {
			// A press that never dragged selected nothing to copy.
			m.clearSelection()
			return m, nil
		}
		m.syncTranscript()
		return m, m.copySelectionCmd()
	}
	return m, nil
}

// dragMargin reports which edge of the transcript a drag is holding the pointer
// against: -1 for the top, 1 for the bottom, 0 for anywhere inside. A pointer
// pulled past the panel counts as being on the edge it left by.
func (m *chatModel) dragMargin() int {
	if len(m.areas) <= 1 {
		return 0
	}
	area := m.areas[1]
	switch {
	case m.dragY <= area.y:
		return -1
	case m.dragY >= area.y+area.height-1:
		return 1
	default:
		return 0
	}
}

// startDragScroll schedules the edge scroll a drag needs, unless one is already
// running. Only one timer is ever in flight, so a pointer held on an edge scrolls
// at the tick's rate rather than at the rate motion events arrive.
func (m *chatModel) startDragScroll() tea.Cmd {
	if !m.selecting || m.dragScrolling || m.dragMargin() == 0 {
		return nil
	}
	m.dragScrolling = true
	return dragScrollTickCmd()
}

// dragScrollStep advances a drag that is holding the pointer against an edge, and
// reschedules itself for as long as it can keep going. It is what lets a
// selection reach past the screen: the view moves by a line and the cursor stays
// under the pointer, so the selection grows by that line.
func (m *chatModel) dragScrollStep() tea.Cmd {
	direction := m.dragMargin()
	if !m.selecting || m.modal != nil || direction == 0 {
		m.dragScrolling = false
		return nil
	}
	width := m.transcriptWidth()
	before := m.anchor
	if direction < 0 {
		m.anchor = m.lineUp(m.anchor, width)
	} else {
		m.anchor = m.lineDown(m.anchor, width)
	}
	m.extendSelection()
	if m.anchor == before {
		// The view could not move: the transcript ends here. The check is made
		// after the sync because that is what decides — at the last line the
		// anchor steps onto the separator past it, and the sync pins the view
		// back where it was rather than leaving a gap under the text.
		m.dragScrolling = false
		return nil
	}
	return dragScrollTickCmd()
}

// extendSelection drags the selection cursor to wherever the pointer is. It is
// what makes a scroll grow the selection instead of only moving the view.
func (m *chatModel) extendSelection() {
	if m.selection == nil {
		return
	}
	if point, ok := m.selectionPointAt(m.dragX, m.dragY); ok {
		m.selection.cursor = point
	}
	m.syncTranscript()
}

// dragScrollTickMsg advances an edge-held drag.
type dragScrollTickMsg struct{}

// dragScrollInterval is how fast an edge-held drag walks the transcript. It is
// the only rate the scroll runs at, since the timer, not the pointer, drives it.
const dragScrollInterval = 40 * time.Millisecond

func dragScrollTickCmd() tea.Cmd {
	return tea.Tick(dragScrollInterval, func(time.Time) tea.Msg { return dragScrollTickMsg{} })
}

// clearSelection drops the selection and redraws the rows it covered.
func (m *chatModel) clearSelection() {
	if m.selection == nil {
		return
	}
	m.selection = nil
	m.selecting = false
	m.syncTranscript()
}

// selectionPointAt maps a screen position to a caret in the transcript. A
// position past the edges of the transcript is clamped to them, so a drag that
// leaves the panel still selects up to the last line it reached.
func (m *chatModel) selectionPointAt(x, y int) (selectionPoint, bool) {
	if len(m.areas) <= 1 || len(m.windowLines) == 0 {
		return selectionPoint{}, false
	}
	area := m.areas[1]
	y = clamp(y, area.y, area.y+max(area.height-1, 0))
	row := m.transcriptOffset + (y - area.y)
	entry, line, ok := m.window.entryAt(row)
	if !ok || entry.block >= len(m.blocks) {
		return selectionPoint{}, false
	}
	column := x - m.viewport.Style.GetPaddingLeft()
	column = clamp(column, 0, max(area.width-m.viewport.Style.GetHorizontalFrameSize(), 1))
	if row >= 0 && row < len(m.windowLines) {
		column = min(column, xansi.StringWidth(m.windowLines[row]))
	}
	return selectionPoint{block: entry.block, line: line, col: column}, true
}

// selectionText is the plain text the selection covers, and how many lines that
// is. The lines are cut out of the rendered blocks, so what reaches the
// clipboard is what the screen showed. Two things the renderer adds are undone,
// because neither survives a paste: it pads every line to the wrap width with
// blanks the reader never selected, and it pads inline code with a non-breaking
// space, which a shell does not read as a delimiter.
func (m *chatModel) selectionText(blocks []block, from, to selectionPoint) (string, int) {
	// The endpoints arrive by value and the span is built here, so the cut reads
	// nothing the live model can move under it: it runs while the reader can
	// already be starting the next drag.
	span := selection{anchor: from, cursor: to}
	width := m.transcriptWidth()
	var copied []string
	last := -1
	for index := max(from.block, 0); index <= to.block && index < len(blocks); index++ {
		entry := &blocks[index]
		content := entry.rendered
		if !entry.renderedValid || entry.renderedWidth != width {
			content = strings.Trim(m.renderBlock(entry, width, false), "\n")
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if last >= 0 {
			// Two blocks are separated by a blank line on screen, so a copy that
			// spans both has to carry it or it is not what the reader saw.
			copied = append(copied, "")
		}
		last = index
		for i, line := range strings.Split(content, "\n") {
			indent := 0
			if i > 0 {
				indent = m.blockIndent(entry)
			}
			start, end, ok := span.span(index, i, xansi.StringWidth(line), indent)
			if !ok {
				continue
			}
			text := strings.TrimRight(xansi.Strip(xansi.Cut(line, start, end)), " \t")
			copied = append(copied, strings.ReplaceAll(text, "\u00a0", " "))
		}
	}
	text := strings.TrimRight(strings.Join(copied, "\n"), " \t\n")
	if text == "" {
		return "", 0
	}
	return text, strings.Count(text, "\n") + 1
}

// paintSelection draws the selection band over the assembled window. It runs on
// the window content, never on a block's cache, so a block stays rendered once
// however often the band moves across it.
func (m *chatModel) paintSelection() string {
	lines := make([]string, len(m.windowLines))
	copy(lines, m.windowLines)
	for _, entry := range m.window.entries {
		for line := range entry.lines {
			row := entry.start + line
			if row < 0 || row >= len(lines) {
				continue
			}
			indent := 0
			if line > 0 {
				indent = m.blockIndent(&m.blocks[entry.block])
			}
			start, end, ok := m.selection.span(entry.block, line, xansi.StringWidth(lines[row]), indent)
			if !ok || start >= end {
				continue
			}
			lines[row] = lipgloss.StyleRanges(lines[row], lipgloss.NewRange(start, end, m.styles.selection))
		}
	}
	return strings.Join(lines, "\n")
}

// clipboardCopyMsg reports a finished copy: the text that went out, how much of
// it there was, and which tool carried it.
type clipboardCopyMsg struct {
	generation uint64
	text       string
	lines      int
	tool       string
	err        error
}

// copySelectionCmd cuts the selection out of the transcript and hands it to the
// system clipboard. Cutting renders every block the selection spans, which on a
// long selection is work measured in seconds — far too much for the update loop,
// the same reason a clipboard image is decoded in a command. So the model is
// copied and the cut runs beside it, against a snapshot of the transcript: the
// render cache it fills is the snapshot's, and a turn still streaming keeps
// filling the live one.
func (m *chatModel) copySelectionCmd() tea.Cmd {
	if m.clipboard == nil || m.selection == nil || m.selection.empty() {
		return nil
	}
	snapshot := *m
	blocks := append([]block(nil), m.blocks...)
	from, to := m.selection.ordered()
	if m.copyCancel != nil {
		m.copyCancel()
	}
	m.copyGeneration++
	generation := m.copyGeneration
	ctx, cancel := context.WithCancel(m.ctx)
	m.copyCancel = cancel
	clipboard := m.clipboard
	return func() tea.Msg {
		text, lines := snapshot.selectionText(blocks, from, to)
		if lines == 0 {
			return clipboardCopyMsg{generation: generation}
		}
		tool, err := clipboard.WriteText(ctx, text)
		return clipboardCopyMsg{generation: generation, text: text, lines: lines, tool: tool, err: err}
	}
}

// handleCopied confirms a copy the way every other user-visible outcome is
// confirmed: as an event in the transcript. A machine with no clipboard helper
// is not a failure — the terminal is handed the text over OSC 52, which is what
// makes a copy land across an SSH hop. That path is write-only, though: nothing
// comes back to say the terminal took it, so the line says what was sent rather
// than that it landed.
func (m *chatModel) handleCopied(msg clipboardCopyMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.copyGeneration {
		return m, nil
	}
	if msg.lines == 0 && msg.err == nil {
		return m, nil
	}
	if msg.err != nil {
		m.appendCopyEvent(fmt.Sprintf("Sent %s · OSC 52", copiedLines(msg.lines)))
		return m, tea.SetClipboard(msg.text)
	}
	m.appendCopyEvent(fmt.Sprintf("Copied %s · %s", copiedLines(msg.lines), msg.tool))
	return m, nil
}

func copiedLines(lines int) string {
	if lines == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", lines)
}

// appendCopyEvent adds the confirmation at the bottom of the chat and reveals
// it immediately, matching other transcript events.
func (m *chatModel) appendCopyEvent(text string) {
	m.blocks = append(m.blocks, block{role: "event", text: text})
	m.refreshTranscript(true)
}
