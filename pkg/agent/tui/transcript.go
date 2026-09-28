package tui

import (
	"strings"

	"github.com/google/uuid"
)

// A session's history grows without bound while the screen does not. The
// viewport therefore holds only a window of the transcript: the blocks the
// active screen reaches, plus at least a screen of overscan on either side. A
// scroll shorter than a screen then costs nothing but a redraw, and blocks
// outside the window are never rendered at all — which is what keeps a long
// conversation, or a window resize that invalidates every block rendered at the
// old width, from costing more than the screen it shows.
//
// The window is measured in lines but rendered in blocks: a block that overlaps
// the window is rendered whole.

// transcriptAnchor is the line at the top of the transcript viewport: a block,
// and the line within it. line == height names the blank line that separates the
// block from the next one, so scrolling covers the separators too.
type transcriptAnchor struct {
	block int
	line  int
}

// windowEntry is one block inside the window.
type windowEntry struct {
	block int
	start int // first line of the block inside the window content
	lines int // rendered line count
}

// transcriptWindow is the slice of the rendered transcript the viewport holds.
type transcriptWindow struct {
	content string
	entries []windowEntry
	total   int
}

// offset returns the window line the anchor sits on.
func (w transcriptWindow) offset(anchor transcriptAnchor) int {
	for _, entry := range w.entries {
		if entry.block == anchor.block {
			return entry.start + anchor.line
		}
	}
	return 0
}

// transcriptWidth is the width blocks render at: the viewport less its frame.
func (m *chatModel) transcriptWidth() int {
	return max(m.viewport.Width()-m.viewport.Style.GetHorizontalFrameSize(), 1)
}

// transcriptHeight is the number of transcript lines the viewport shows.
func (m *chatModel) transcriptHeight() int {
	return max(m.viewport.Height()-m.viewport.Style.GetVerticalFrameSize(), 0)
}

// blockRender returns a block's rendering at width and the lines it occupies. A
// block that renders nothing occupies no line at all, not even a separator.
// Index len(blocks) is the expanded decision detail, which rides after the last
// block.
//
// The in-flight block is cached like any other, and invalidated as its text
// grows. A refresh renders it once even though the anchor, the clamp, the
// window and the assembly each ask for it — without that cache each question
// re-rendered the whole message.
func (m *chatModel) blockRender(index, width int) (string, int) {
	if index == len(m.blocks) {
		return m.approvalTail(width)
	}
	if index < 0 || index >= len(m.blocks) {
		return "", 0
	}
	entry := &m.blocks[index]
	if entry.renderedValid && entry.renderedWidth == width {
		return entry.rendered, entry.renderedLines
	}
	streaming := m.streaming && m.streamingAt == index
	content := strings.Trim(m.renderBlock(entry, width, streaming), "\n")
	lines := 0
	if strings.TrimSpace(content) != "" {
		lines = strings.Count(content, "\n") + 1
	}
	entry.rendered, entry.renderedWidth, entry.renderedLines, entry.renderedValid = content, width, lines, true
	return content, lines
}

// approvalTail is the expanded decision detail as transcript text. It is cached
// because the scroll position asks for its height on every frame, and a diff
// render is not free.
func (m *chatModel) approvalTail(width int) (string, int) {
	key := tailKey{width: width, shown: m.approvalShown}
	if m.approval != nil {
		key.id, key.fingerprint = m.approval.ID, m.approval.Fingerprint
	}
	if m.tailValid && m.tailKey == key {
		return m.tailRendered, m.tailLines
	}
	content, lines := "", 0
	if m.approval != nil && m.approvalShown {
		content = strings.Trim(m.approvalDetailBlock(width), "\n")
		if strings.TrimSpace(content) != "" {
			lines = strings.Count(content, "\n") + 1
		}
	}
	m.tailKey, m.tailRendered, m.tailLines, m.tailValid = key, content, lines, true
	return content, lines
}

// tailKey identifies the decision a cached detail render belongs to. The id and
// the fingerprint both move when the decision does; both are carried because a
// decision built without a fingerprint still has an id.
type tailKey struct {
	id          uuid.UUID
	fingerprint string
	width       int
	shown       bool
}

// blockLines is the height of a block in the transcript.
func (m *chatModel) blockLines(index, width int) int {
	_, lines := m.blockRender(index, width)
	return lines
}

// nextBlock returns the next block that renders something, or -1 when the
// transcript ends first.
func (m *chatModel) nextBlock(index, width int) int {
	for i := max(index, 0); i <= len(m.blocks); i++ {
		if m.blockLines(i, width) > 0 {
			return i
		}
	}
	return -1
}

// prevBlock returns the previous block that renders something, or -1 when the
// transcript starts after it.
func (m *chatModel) prevBlock(index, width int) int {
	for i := min(index, len(m.blocks)); i >= 0; i-- {
		if m.blockLines(i, width) > 0 {
			return i
		}
	}
	return -1
}

// lineDown is the position one line below a position, crossing the blank line
// between two blocks on the way.
func (m *chatModel) lineDown(pos transcriptAnchor, width int) transcriptAnchor {
	lines := m.blockLines(pos.block, width)
	if pos.line+1 < lines {
		return transcriptAnchor{block: pos.block, line: pos.line + 1}
	}
	next := m.nextBlock(pos.block+1, width)
	if next < 0 {
		return transcriptAnchor{block: pos.block, line: max(lines-1, 0)}
	}
	if pos.line+1 == lines {
		return transcriptAnchor{block: pos.block, line: lines}
	}
	return transcriptAnchor{block: next, line: 0}
}

// lineUp is the position one line above a position, landing on the blank line
// between two blocks when it crosses a boundary.
func (m *chatModel) lineUp(pos transcriptAnchor, width int) transcriptAnchor {
	if pos.line > 0 {
		return transcriptAnchor{block: pos.block, line: pos.line - 1}
	}
	prev := m.prevBlock(pos.block-1, width)
	if prev < 0 {
		return transcriptAnchor{}
	}
	return transcriptAnchor{block: prev, line: m.blockLines(prev, width)}
}

// bottomAnchor is the anchor that puts the end of the transcript at the bottom
// of the viewport. A transcript shorter than the viewport is anchored at its
// first line instead.
func (m *chatModel) bottomAnchor(width, height int) transcriptAnchor {
	last, lines := m.lastBlock(width)
	if lines == 0 {
		return transcriptAnchor{}
	}
	pos := transcriptAnchor{block: last, line: lines - 1}
	for range max(height-1, 0) {
		previous := m.lineUp(pos, width)
		if previous == pos {
			break
		}
		pos = previous
	}
	return pos
}

// lastBlock is the block whose last line ends the transcript: the expanded
// decision detail when it is shown, the final block otherwise.
func (m *chatModel) lastBlock(width int) (int, int) {
	if _, lines := m.blockRender(len(m.blocks), width); lines > 0 {
		return len(m.blocks), lines
	}
	last := m.prevBlock(len(m.blocks)-1, width)
	if last < 0 {
		return 0, 0
	}
	return last, m.blockLines(last, width)
}

// atBottom reports whether the view shows the newest output. It is what the
// status row means by "viewing earlier output", and what re-pinning follows.
func (m *chatModel) atBottom() bool {
	if m.transcriptHeight() <= 0 {
		return true
	}
	return m.anchor == m.bottomAnchor(m.transcriptWidth(), m.transcriptHeight())
}

// clampAnchor brings an anchor back inside the transcript after the session,
// the width, or the viewport changed under it.
func (m *chatModel) clampAnchor(anchor transcriptAnchor, width, height int) transcriptAnchor {
	if anchor.block < 0 || anchor.block > len(m.blocks) || m.blockLines(anchor.block, width) == 0 {
		if next := m.nextBlock(anchor.block, width); next >= 0 && next != anchor.block {
			return transcriptAnchor{block: next}
		}
		if prev := m.prevBlock(anchor.block-1, width); prev >= 0 {
			return transcriptAnchor{block: prev, line: m.blockLines(prev, width)}
		}
		return transcriptAnchor{}
	}
	if lines := m.blockLines(anchor.block, width); anchor.line > lines {
		return transcriptAnchor{block: anchor.block, line: lines}
	}
	return anchor
}

// scrollTranscript moves the top of the view by delta lines, stopping at the
// ends of the transcript. Scrolling belongs to the model: the viewport only
// renders the window it is handed, so the scroll position never depends on how
// much history that window happens to hold.
func (m *chatModel) scrollTranscript(delta int) {
	width := m.transcriptWidth()
	steps := max(delta, -delta)
	for range steps {
		next := m.lineDown(m.anchor, width)
		if delta < 0 {
			next = m.lineUp(m.anchor, width)
		}
		if next == m.anchor {
			break
		}
		m.anchor = next
	}
	m.syncTranscript()
}

// followTranscript pins the view to the newest output.
func (m *chatModel) followTranscript() {
	m.anchor = m.bottomAnchor(m.transcriptWidth(), m.transcriptHeight())
	m.syncTranscript()
}

// syncTranscript renders the window around the anchor and hands it to the
// viewport. It is the only writer of the viewport's content.
func (m *chatModel) syncTranscript() {
	height := m.transcriptHeight()
	if m.viewport.Width() <= 0 || height <= 0 {
		return
	}
	width := m.transcriptWidth()
	m.anchor = m.clampAnchor(m.anchor, width, height)
	window := m.buildWindow(width, height)
	offset := window.offset(m.anchor)
	if window.total > height && offset > window.total-height {
		// The anchor sits past the end of the transcript — the viewport shrank
		// under it, or a reflow left fewer lines below it — so the view pins to
		// the end instead of leaving a gap under the last line.
		if bottom := m.bottomAnchor(width, height); bottom != m.anchor {
			m.anchor = bottom
			window = m.buildWindow(width, height)
			offset = window.offset(m.anchor)
		}
	}
	m.viewport.SetContent(window.content)
	m.viewport.SetYOffset(offset)
}

// buildWindow renders the blocks the viewport needs: the anchor's block, enough
// above it to cover a screen of overscan, and enough below to fill the viewport
// and a screen more.
func (m *chatModel) buildWindow(width, height int) transcriptWindow {
	first, last := m.anchor.block, m.anchor.block
	for i, lines := first-1, 0; i >= 0 && lines < height; i-- {
		if blockLines := m.blockLines(i, width); blockLines > 0 {
			lines += blockLines + 1
			first = i
		}
	}
	for i, lines := last+1, 0; i <= len(m.blocks) && lines < 2*height; i++ {
		if blockLines := m.blockLines(i, width); blockLines > 0 {
			lines += blockLines + 1
			last = i
		}
	}
	return m.assembleWindow(width, first, last)
}

// assembleWindow renders blocks first through last into the content the
// viewport shows, recording where each one starts so the anchor can be placed.
func (m *chatModel) assembleWindow(width, first, last int) transcriptWindow {
	window := transcriptWindow{}
	var parts []string
	for index := first; index <= last; index++ {
		content, lines := m.blockRender(index, width)
		if lines == 0 {
			continue
		}
		start := 0
		if count := len(window.entries); count > 0 {
			previous := window.entries[count-1]
			start = previous.start + previous.lines + 1
		}
		window.entries = append(window.entries, windowEntry{block: index, start: start, lines: lines})
		parts = append(parts, content)
	}
	if count := len(window.entries); count > 0 {
		last := window.entries[count-1]
		window.total = last.start + last.lines
	}
	window.content = strings.Join(parts, "\n\n")
	if len(window.entries) == 0 {
		window.content = m.styles.muted.Render("\nStart with a question, a task, or / for commands.")
	}
	return window
}
