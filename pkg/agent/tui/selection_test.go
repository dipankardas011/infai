package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/google/uuid"
)

// recordingClipboard is a Clipboard that remembers what a copy handed it.
type recordingClipboard struct {
	text  string
	tool  string
	err   error
	reads int
}

func (c *recordingClipboard) ReadImage(context.Context) ([]byte, error) {
	c.reads++
	return nil, ErrClipboardEmpty
}

func (c *recordingClipboard) WriteText(_ context.Context, text string) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	c.text = text
	tool := c.tool
	if tool == "" {
		tool = "test-copy"
	}
	return tool, nil
}

// selectableChat builds a sized model whose transcript is the given blocks, with
// the modal the transcript lives behind dismissed and a clipboard that records.
func selectableChat(t *testing.T, clipboard Clipboard, blocks ...block) *chatModel {
	t.Helper()
	m := newChatModel(context.Background(), nil, nil, RunOptions{})
	m.modal = nil
	m.blocks = append(m.blocks, blocks...)
	if clipboard != nil {
		m.clipboard = clipboard
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	if len(m.areas) <= 1 || m.areas[1].height == 0 {
		t.Fatalf("model has no transcript area: %+v", m.areas)
	}
	return m
}

// screenAt is the screen position of the first visible occurrence of needle in
// the transcript, so a test can point the mouse at text it knows is on screen.
func screenAt(t *testing.T, m *chatModel, needle string) (int, int) {
	t.Helper()
	area := m.areas[1]
	padding := m.viewport.Style.GetPaddingLeft()
	for row, line := range m.windowLines {
		plain := xansi.Strip(line)
		before, _, ok := strings.Cut(plain, needle)
		if !ok {
			continue
		}
		y := area.y + (row - m.transcriptOffset)
		if y < area.y || y >= area.y+area.height {
			continue
		}
		// Index counts bytes; the pointer counts cells, and the harness draws
		// markers the test's needle does not contain.
		return xansi.StringWidth(before) + padding, y
	}
	t.Fatalf("transcript does not show %q:\n%s", needle, strings.Join(m.windowLines, "\n"))
	return 0, 0
}

func drag(t *testing.T, m *chatModel, fromX, fromY, toX, toY int) tea.Cmd {
	t.Helper()
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: fromX, Y: fromY, Button: tea.MouseLeft}))
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: toX, Y: toY, Button: tea.MouseLeft}))
	_, cmd := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: toX, Y: toY, Button: tea.MouseNone}))
	return cmd
}

// TestSelectionCopiesWhatTheScreenShows pins the answer to "does a copy take the
// rendered markdown or the raw markdown": the transcript holds glamour's output,
// so that — and only that — is what a mouse can reach. The markers the harness
// draws, the list bullets glamour substitutes and the emphasis it drops are all
// part of the line the pointer lands on.
// selectedText reads the copy the way the command builds it: from a snapshot of
// the transcript, at the current width.
func selectedText(t *testing.T, m *chatModel) (string, int) {
	t.Helper()
	if m.selection == nil {
		return "", 0
	}
	from, to := m.selection.ordered()
	return m.selectionText(append([]block(nil), m.blocks...), from, to)
}

func TestSelectionCopiesWhatTheScreenShows(t *testing.T) {
	raw := "Here is **bold** and `code`.\n\n- first item\n- second item\n"
	m := selectableChat(t, nil, block{role: "assistant", text: raw})
	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0},
		cursor: selectionPoint{block: 0, line: 4, col: 60},
	}
	copied, lines := selectedText(t, m)

	if strings.Contains(copied, "**") || strings.Contains(copied, "- first") {
		t.Fatalf("copy took raw markdown: %q", copied)
	}
	if !strings.HasPrefix(copied, "● Here is bold and ") {
		t.Fatalf("copy lacks the rendered first line: %q", copied)
	}
	if !strings.Contains(copied, "code") || strings.Contains(copied, "`code`") {
		t.Fatalf("copy did not take the rendered inline code: %q", copied)
	}
	if !strings.Contains(copied, "• first item") {
		t.Fatalf("copy lacks glamour's list bullet: %q", copied)
	}
	if strings.Contains(copied, "  \n") {
		t.Fatalf("copy kept the render's trailing padding: %q", copied)
	}
	if strings.ContainsRune(copied, '\u00a0') {
		t.Fatalf("copy kept the renderer's non-breaking padding around inline code: %q", copied)
	}
	if lines != 4 {
		t.Fatalf("lines = %d, want 4:\n%s", lines, copied)
	}
}

func TestMouseDragCopiesTheSelectedText(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	x, y := screenAt(t, m, "world")
	cmd := drag(t, m, x, y, x+5, y)
	if cmd == nil {
		t.Fatal("release did not copy the selection")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("copy command produced no message")
	}
	if clipboard.text != "world" {
		t.Fatalf("clipboard = %q, want %q", clipboard.text, "world")
	}
	if m.selection == nil {
		t.Fatal("selection was cleared before the copy landed")
	}
}

func TestCopiedSelectionIsAnnouncedAsAnEvent(t *testing.T) {
	clipboard := &recordingClipboard{tool: "wl-copy"}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})
	before := len(m.blocks)

	x, y := screenAt(t, m, "world")
	cmd := drag(t, m, x, y, x+5, y)

	_, _ = m.Update(cmd())

	if len(m.blocks) != before+1 {
		t.Fatalf("blocks = %d, want one event appended", len(m.blocks))
	}
	event := m.blocks[len(m.blocks)-1]
	if event.role != "event" {
		t.Fatalf("confirmation role = %q, want event", event.role)
	}
	if event.text != "Copied 1 line · wl-copy" {
		t.Fatalf("confirmation = %q", event.text)
	}
}

// TestCopyFallsBackToOSC52 covers a machine with no clipboard helper: the text
// still has to reach the terminal, which is what carries a copy over SSH.
func TestCopyFallsBackToOSC52(t *testing.T) {
	clipboard := &recordingClipboard{err: ErrClipboardUnavailable}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	x, y := screenAt(t, m, "world")
	cmd := drag(t, m, x, y, x+5, y)
	if cmd == nil {
		t.Fatal("release did not copy the selection")
	}
	_, fallback := m.Update(cmd())
	if fallback == nil {
		t.Fatal("a failed helper did not fall back to OSC 52")
	}
	if event := m.blocks[len(m.blocks)-1]; event.text != "Sent 1 line · OSC 52" {
		t.Fatalf("confirmation = %q", event.text)
	}
}

// channelClipboard hands each text a copy wrote to the test over a channel, so
// nothing has to poll a field the program goroutine is writing.
type channelClipboard struct{ written chan string }

func (c *channelClipboard) ReadImage(context.Context) ([]byte, error) {
	return nil, ErrClipboardEmpty
}

func (c *channelClipboard) WriteText(_ context.Context, text string) (string, error) {
	c.written <- text
	return "test-copy", nil
}

// TestMouseDragThroughTheTerminalSequence drives the real input decoder rather
// than synthesised messages: a terminal reports a drag as SGR mouse escapes, and
// this pins that those bytes become a copy of exactly what they covered.
func TestMouseDragThroughTheTerminalSequence(t *testing.T) {
	written := make(chan string, 1)
	m := selectableChat(t, &channelClipboard{written: written}, block{role: "user", text: "hello world"})
	x, y := screenAt(t, m, "world")

	input, terminal := io.Pipe()
	program := tea.NewProgram(m,
		tea.WithInput(input),
		tea.WithOutput(io.Discard),
		tea.WithWindowSize(60, 16),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = program.Run()
	}()
	defer func() {
		program.Quit()
		<-done
	}()

	for _, sequence := range []string{
		fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1),  // press on "world"
		fmt.Sprintf("\x1b[<32;%d;%dM", x+6, y+1), // drag to the end of the line
		fmt.Sprintf("\x1b[<0;%d;%dm", x+6, y+1),  // release
	} {
		if _, err := io.WriteString(terminal, sequence); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case text := <-written:
		if text != "world" {
			t.Fatalf("copied %q, want %q", text, "world")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a drag reported as SGR mouse escapes never reached the clipboard")
	}
}

// TestMousePressOutsideTheTranscriptSelectsNothing guards the panel boundary. A
// press on the header, the composer or the status row must not be clamped into
// the transcript, or a small drag there would copy whichever line happens to sit
// under the panel and write an event into the conversation.
func TestMousePressOutsideTheTranscriptSelectsNothing(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	if m.areas[1].y == 0 {
		t.Fatalf("the transcript starts at the top of the screen: %+v", m.areas[1])
	}
	for _, y := range []int{0, m.height - 1} {
		before := len(m.blocks)
		if cmd := drag(t, m, 4, y, 12, y); cmd != nil {
			t.Fatalf("a drag on screen row %d copied transcript text", y)
		}
		if clipboard.text != "" {
			t.Fatalf("a drag on screen row %d reached the clipboard: %q", y, clipboard.text)
		}
		if len(m.blocks) != before {
			t.Fatalf("a drag on screen row %d appended a transcript event", y)
		}
	}
}

// TestDragLeavingTheTranscriptStillCopies is the other half of the boundary: the
// press decides whether a drag is a selection, and once it is one, the pointer is
// free to leave the panel without the copy being dropped.
func TestDragLeavingTheTranscriptStillCopies(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	x, y := screenAt(t, m, "world")
	cmd := drag(t, m, x, y, x+5, m.height-1)
	if cmd == nil {
		t.Fatal("a drag that left the transcript did not copy")
	}
	_ = cmd()
	if clipboard.text != "world" {
		t.Fatalf("clipboard = %q, want %q", clipboard.text, "world")
	}
}

// TestDragOnThePositionTheViewDrew checks the mapping against the composed frame
// rather than against the model's own arithmetic. The needle is located in
// m.View(), which is what the terminal is handed, so an off-by-one shared
// between the hit test and the test helper cannot hide from this test.
func TestDragOnThePositionTheViewDrew(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	y, x := -1, 0
	for row, line := range strings.Split(m.View().Content, "\n") {
		plain := xansi.Strip(line)
		index := strings.Index(plain, "world")
		if index < 0 {
			continue
		}
		y, x = row, xansi.StringWidth(plain[:index])
		break
	}
	if y < 0 {
		t.Fatalf("the composed view does not show the text:\n%s", m.View().Content)
	}

	cmd := drag(t, m, x, y, x+5, y)
	if cmd == nil {
		t.Fatal("a drag on the position the view drew did not copy")
	}
	_ = cmd()
	if clipboard.text != "world" {
		t.Fatalf("clipboard = %q, want %q", clipboard.text, "world")
	}
}

// TestResizeDropsTheSelection covers the one thing that invalidates a selection
// without replacing the transcript: a new width re-wraps every rendered line, so
// the cells the selection named no longer exist.
func TestResizeDropsTheSelection(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, block{role: "user", text: "hello world"})
	x, y := screenAt(t, m, "world")
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x + 5, Y: y, Button: tea.MouseLeft}))
	if m.selection == nil || !m.selecting {
		t.Fatal("the drag did not start a selection")
	}

	_, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})

	if m.selection != nil || m.selecting {
		t.Fatal("a selection outlived the width it was measured at")
	}
	if content := m.viewport.GetContent(); strings.Contains(content, "\x1b[48;2;66;80;71m") {
		t.Fatal("the band is still in the viewport after a resize")
	}
}

// TestWheelDuringADragCarriesTheSelection covers the wheel as a scroll that the
// drag follows: the pointer has not moved, so whatever it now points at is what
// the selection has reached. That is how a drag gets at text that is not on
// screen.
func TestWheelDuringADragCarriesTheSelection(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, numberedBlocks(200)...)
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()

	x, y := screenAt(t, m, "event 5")
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	for range 10 {
		_, _ = m.Update(tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseWheelDown}))
	}

	text, lines := selectedText(t, m)
	if lines <= m.areas[1].height {
		t.Fatalf("the wheel did not carry the selection past the screen: %d lines for a %d-row transcript", lines, m.areas[1].height)
	}
	if !strings.Contains(text, "event 5") || !strings.Contains(text, "event 15") {
		t.Fatalf("copy does not span from the anchor to what the wheel reached: %q", text)
	}
}

// TestEdgeScrollExtendsTheSelection is the drag-to-the-edge gesture: holding the
// pointer on the last row walks the transcript and grows the selection with it,
// which is the only way a selection can reach past the screen without the wheel.
func TestEdgeScrollExtendsTheSelection(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, numberedBlocks(200)...)
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()

	x, y := screenAt(t, m, "event 0")
	if y != m.areas[1].y {
		t.Fatalf("the first block is not on the top row: y=%d area=%+v", y, m.areas[1])
	}
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))

	edge := m.areas[1].y + m.areas[1].height - 1
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseLeft}))
	if !m.dragScrolling {
		t.Fatal("holding the pointer against an edge did not start an edge scroll")
	}
	for range 20 {
		_, _ = m.Update(dragScrollTickMsg{})
	}
	if m.anchor == (transcriptAnchor{block: 0}) {
		t.Fatal("the edge scroll did not walk the transcript")
	}

	_, release := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseNone}))
	if release == nil {
		t.Fatal("a release after an edge scroll did not copy")
	}
	msg := release().(clipboardCopyMsg)
	if msg.lines <= m.areas[1].height {
		t.Fatalf("edge scroll copied %d lines, want more than the %d on screen", msg.lines, m.areas[1].height)
	}
	// The screen showed blocks 0..5 when the drag started, so reaching 10 is the
	// proof that the selection walked past it.
	for _, want := range []string{"event 0", "event 10"} {
		if !strings.Contains(msg.text, want) {
			t.Fatalf("copy does not reach %s:\n%s", want, msg.text)
		}
	}
}

// TestEdgeScrollChainsThroughItsCommands drives the commands the way the program
// does — motion returns a tick, the tick returns the next tick — so the timer is
// shown to sustain itself, rather than the test feeding itself messages.
func TestEdgeScrollChainsThroughItsCommands(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, numberedBlocks(200)...)
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()

	x, y := screenAt(t, m, "event 3")
	edge := m.areas[1].y + m.areas[1].height - 1
	if y == m.areas[1].y || y == edge {
		t.Fatalf("the pointer has to start inside the panel, not on an edge row: y=%d area=%+v", y, m.areas[1])
	}
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))

	_, cmd := m.Update(tea.MouseMotionMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseLeft}))
	if cmd == nil {
		t.Fatal("holding the pointer against the last row scheduled no scroll")
	}
	for i := range 12 {
		msg := cmd()
		if _, ok := msg.(dragScrollTickMsg); !ok {
			t.Fatalf("tick %d produced %#v, want a dragScrollTickMsg", i, msg)
		}
		_, cmd = m.Update(msg)
	}
	if cmd == nil {
		t.Fatal("the edge scroll stopped before the selection reached past the screen")
	}

	_, release := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseNone}))
	if release == nil {
		t.Fatal("the release did not copy")
	}
	msg := release().(clipboardCopyMsg)
	if msg.lines <= m.areas[1].height {
		t.Fatalf("copied %d lines after an edge scroll, want more than the %d on screen", msg.lines, m.areas[1].height)
	}
	if !strings.Contains(msg.text, "event 3") || !strings.Contains(msg.text, "event 10") {
		t.Fatalf("copy does not span from the anchor past the first screen:\n%s", msg.text)
	}
}

func TestEdgeScrollStopsWhenThePointerLeavesTheEdge(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, numberedBlocks(200)...)
	x, y := screenAt(t, m, "event 197")
	if y == m.areas[1].y || y == m.areas[1].y+m.areas[1].height-1 {
		t.Fatalf("the pointer has to start inside the panel, not on an edge row: y=%d area=%+v", y, m.areas[1])
	}

	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	edge := m.areas[1].y + m.areas[1].height - 1
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseLeft}))
	if _, cmd := m.Update(tea.MouseMotionMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft})); cmd != nil {
		t.Fatal("a motion inside the panel scheduled an edge scroll")
	}

	before := m.anchor
	_, _ = m.Update(dragScrollTickMsg{})
	if m.anchor != before {
		t.Fatal("the edge timer kept scrolling after the pointer left the edge")
	}
	if m.dragScrolling {
		t.Fatal("the edge timer was left running")
	}
}

func TestEdgeScrollStopsAtTheEndOfTheTranscript(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, numberedBlocks(40)...)
	if !m.atBottom() {
		t.Fatal("the transcript did not open at its newest output")
	}
	x, y := screenAt(t, m, "event 39")

	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	edge := m.areas[1].y + m.areas[1].height - 1
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x, Y: edge, Button: tea.MouseLeft}))

	before := m.anchor
	_, _ = m.Update(dragScrollTickMsg{})
	if m.anchor != before {
		t.Fatal("the edge scroll ran past the end of the transcript")
	}
	if m.dragScrolling {
		t.Fatal("the edge timer kept running at the end of the transcript")
	}
}

// numberedBlocks is a transcript of one-line blocks, which makes the line
// arithmetic of a selection easy to assert on.
func numberedBlocks(count int) []block {
	blocks := make([]block, 0, count)
	for i := range count {
		blocks = append(blocks, block{role: "system", text: fmt.Sprintf("event %d", i)})
	}
	return blocks
}

func TestMouseClickWithoutDragCopiesNothing(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})

	x, y := screenAt(t, m, "world")
	if cmd := drag(t, m, x, y, x, y); cmd != nil {
		t.Fatal("a click copied a selection it never made")
	}
	if clipboard.text != "" {
		t.Fatalf("clipboard = %q, want nothing", clipboard.text)
	}
	if m.selection != nil {
		t.Fatal("a click left a selection behind")
	}
}

func TestSelectionHighlightKeepsEveryLineItsWidth(t *testing.T) {
	m := selectableChat(t, nil, block{role: "assistant", text: "one **two** three\n\nfour\n"})
	plain := append([]string(nil), m.windowLines...)

	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0, col: 2},
		cursor: selectionPoint{block: 0, line: 2, col: 4},
	}
	painted := strings.Split(m.windowContent(), "\n")

	if len(painted) != len(plain) {
		t.Fatalf("selection changed the line count: %d -> %d", len(plain), len(painted))
	}
	for i := range plain {
		if width, want := xansi.StringWidth(painted[i]), xansi.StringWidth(plain[i]); width != want {
			t.Fatalf("line %d width %d, want %d: %q", i, width, want, painted[i])
		}
		if got, want := xansi.Strip(painted[i]), xansi.Strip(plain[i]); got != want {
			t.Fatalf("line %d text changed:\n got %q\nwant %q", i, got, want)
		}
	}
	if !strings.Contains(painted[0], "\x1b[48;2;66;80;71m") {
		t.Fatalf("the selected line carries no band: %q", painted[0])
	}
}

// selectionBand is the escape the selection style paints with (Everforest
// SelectionBg), used to find where the band starts on a rendered row.
const selectionBand = "\x1b[48;2;66;80;71m"

// TestParagraphCopyDropsTheHarnessIndent is the paragraph case: dragging from the
// left edge of an answer used to carry the two columns the harness aligns under
// the marker — in the band and on the clipboard, on every wrapped line.
func TestParagraphCopyDropsTheHarnessIndent(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, block{
		role: "assistant",
		text: strings.Repeat("alpha bravo charlie delta echo ", 6),
	})
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()

	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0},
		cursor: selectionPoint{block: 0, line: 99, col: 99},
	}
	copied, lines := selectedText(t, m)
	if lines < 3 {
		t.Fatalf("the paragraph did not wrap to several lines: %q", copied)
	}
	for _, line := range strings.Split(copied, "\n") {
		if strings.HasPrefix(line, " ") {
			t.Fatalf("a copied line still carries the harness indent: %q\n---\n%s", line, copied)
		}
	}

	painted := strings.Split(m.windowContent(), "\n")
	before, _, found := strings.Cut(painted[1], selectionBand)
	if !found {
		t.Fatalf("the second row carries no band: %q", painted[1])
	}
	if got := xansi.StringWidth(xansi.Strip(before)); got != m.blockIndent(&m.blocks[0]) {
		t.Fatalf("the band starts %d cells in, want the %d the harness aligns with: %q", got, m.blockIndent(&m.blocks[0]), painted[1])
	}
}

// TestCopyDoesNotClipLinesThatWrapWithoutIndent is the other half of the rule. An
// event, system or error block wraps at the left edge, so nothing may be taken
// off it: reading the indent off the role, rather than assuming every block is
// aligned under a marker, is what keeps a wrapped line from losing characters.
func TestCopyDoesNotClipLinesThatWrapWithoutIndent(t *testing.T) {
	text := strings.Repeat("alpha bravo charlie delta echo ", 6)
	m := selectableChat(t, &recordingClipboard{}, block{role: "event", text: text})
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()

	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0},
		cursor: selectionPoint{block: 0, line: 99, col: 99},
	}
	copied, _ := selectedText(t, m)

	if got, want := strings.Join(strings.Fields(copied), " "), strings.Join(strings.Fields("◆ "+text), " "); got != want {
		t.Fatalf("copy lost or mangled text:\n got %q\nwant %q", got, want)
	}
}

// TestBlockIndentMatchesWhatEachRoleRenders pins the one fact the selection rule
// needs from the renderers: which roles indent their continuation lines. If a
// renderer starts or stops indenting, this fails here instead of the copy
// silently eating characters off a wrapped line.
func TestBlockIndentMatchesWhatEachRoleRenders(t *testing.T) {
	paragraph := strings.Repeat("alpha bravo charlie delta echo ", 6)
	for _, entry := range []block{
		{role: "user", text: paragraph},
		{role: "assistant", text: paragraph},
		{role: "thinking", text: paragraph},
		{role: "skill", text: paragraph},
		{role: "event", text: paragraph},
		{role: "system", text: paragraph},
		{role: "error", text: paragraph},
		{role: "compaction", text: paragraph},
		{role: "tool", toolKind: "call", toolName: string(contracts.BashTool), toolArgs: `{"command":"echo ` + paragraph + `"}`},
	} {
		m := selectableChat(t, nil, entry)
		content, lines := m.blockRender(0, m.transcriptWidth())
		if lines < 2 {
			t.Fatalf("role %q did not wrap to a second line", entry.role)
		}
		second := strings.Split(content, "\n")[1]
		got, want := len(second)-len(strings.TrimLeft(second, " ")), m.blockIndent(&m.blocks[0])
		if got != want {
			t.Fatalf("role %q indents its second line by %d, blockIndent says %d: %q", entry.role, got, want, second)
		}
	}
}

// TestFirstSelectionAfterJoiningSurvivesTheSessionView is the first-drag bug:
// joining a session delivers that session's own view, and a drag that was in
// flight when it landed used to be thrown away with the transcript it was made
// on — so the release had nothing to copy. The next drag worked because no view
// followed it.
func TestFirstSelectionAfterJoiningSurvivesTheSessionView(t *testing.T) {
	clipboard := &recordingClipboard{}
	m := selectableChat(t, clipboard, block{role: "user", text: "hello world"})
	m.session = store.SessionMeta{ID: uuid.New(), Model: "deepseek-flash"}
	m.startSessionObserver(m.session.ID)

	x, y := screenAt(t, m, "world")
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x + 5, Y: y, Button: tea.MouseLeft}))
	if !m.selecting {
		t.Fatal("the drag did not start")
	}

	content := "hello world"
	_, _ = m.Update(sessionViewMsg{
		sessionID:  m.session.ID,
		observerID: m.sessionObserverID,
		view: glue.SessionView{
			Meta:    m.session,
			History: []contracts.ChatMessage{{Role: "user", Content: &content}},
		},
	})

	_, cmd := m.Update(tea.MouseReleaseMsg(tea.Mouse{X: x + 5, Y: y, Button: tea.MouseNone}))
	if cmd == nil {
		t.Fatal("the session view ate the first selection of the session")
	}
	_ = cmd()
	if clipboard.text != "world" {
		t.Fatalf("clipboard = %q, want %q", clipboard.text, "world")
	}
}

// TestSessionViewDoesNotScrollOutFromUnderADrag is the other half of that race:
// the view arriving on a join must not pull the transcript to its newest line
// while the pointer is down, or the release would copy a different range than
// the one being dragged.
func TestSessionViewDoesNotScrollOutFromUnderADrag(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{})
	m.session = store.SessionMeta{ID: uuid.New(), Model: "deepseek-flash"}
	m.startSessionObserver(m.session.ID)

	// A transcript taller than the screen, built from a history so the view
	// re-applied below is that same one.
	var history []contracts.ChatMessage
	for i := range 60 {
		text := fmt.Sprintf("message %d %s", i, strings.Repeat("alpha bravo ", 8))
		history = append(history, contracts.ChatMessage{Role: "user", Content: &text})
	}
	view := glue.SessionView{Meta: m.session, History: history}
	m.applySessionView(view)
	m.anchor = transcriptAnchor{}
	m.syncTranscript()
	if m.atBottom() {
		t.Fatal("the transcript did not scroll back for the test")
	}
	before := m.transcriptOffset

	x, y := screenAt(t, m, "message 0")
	_, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}))
	_, _ = m.Update(tea.MouseMotionMsg(tea.Mouse{X: x + 3, Y: y, Button: tea.MouseLeft}))
	_, _ = m.Update(sessionViewMsg{sessionID: m.session.ID, observerID: m.sessionObserverID, view: view})

	if m.transcriptOffset != before {
		t.Fatalf("the session view scrolled the transcript under the drag: %d -> %d", before, m.transcriptOffset)
	}
	if m.selection == nil {
		t.Fatal("the session view dropped the selection")
	}
}

// TestCopyDoesNotRenderTheTranscriptOnTheUpdateLoop pins the fix for the freeze.
// Cutting a selection renders every block it spans, and a long selection made by
// scrolling is thousands of lines: doing that on the update loop stopped the
// harness drawing until it finished. The release hands the work to its command
// and leaves the transcript exactly as it found it.
func TestCopyDoesNotRenderTheTranscriptOnTheUpdateLoop(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, numberedBlocks(600)...)
	m.anchor = transcriptAnchor{block: 0}
	m.syncTranscript()
	if m.blocks[500].renderedValid {
		t.Fatal("the fixture had the whole transcript rendered already")
	}

	// The selection a long scroll makes: blocks the screen never reached.
	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0},
		cursor: selectionPoint{block: 599, line: 9, col: 40},
	}
	cmd := m.copySelectionCmd()
	if cmd == nil {
		t.Fatal("the release did not schedule a copy")
	}
	if m.blocks[500].renderedValid {
		t.Fatal("the copy rendered the transcript on the update loop")
	}

	msg := cmd().(clipboardCopyMsg)
	if !strings.Contains(msg.text, "event 500") {
		t.Fatalf("the cut does not reach the blocks it spans: %d lines", msg.lines)
	}
	// The cut renders into the snapshot it was given: the live transcript keeps
	// the cache it had, so a megabyte of rendered text is not kept alive by a
	// copy the reader has already taken.
	if m.blocks[500].renderedValid {
		t.Fatal("the cut wrote its render cache back into the live transcript")
	}
}

func TestSelectionSpanAcrossBlocks(t *testing.T) {
	m := selectableChat(t, nil,
		block{role: "user", text: "first"},
		block{role: "user", text: "second"},
	)
	m.selection = &selection{
		anchor: selectionPoint{block: 0, line: 0, col: 2},
		cursor: selectionPoint{block: 1, line: 0, col: 5},
	}
	copied, _ := selectedText(t, m)
	if copied != "first\n\n● sec" {
		t.Fatalf("copy = %q, want the tail of one block, the blank line between them, and the head of the next", copied)
	}
}

func TestSelectionSpanIsOrderIndependent(t *testing.T) {
	forward := selection{anchor: selectionPoint{block: 0, line: 0, col: 2}, cursor: selectionPoint{block: 0, line: 0, col: 6}}
	backward := selection{anchor: forward.cursor, cursor: forward.anchor}
	start, end, ok := backward.span(0, 0, 20, 0)
	if !ok || start != 2 || end != 6 {
		t.Fatalf("span = (%d, %d, %v), want a drag up to read like a drag down", start, end, ok)
	}
}

func TestSelectionPointSnapsToTheNearestBlock(t *testing.T) {
	m := selectableChat(t, nil,
		block{role: "user", text: "first"},
		block{role: "user", text: "second"},
	)
	first := m.window.entries[0]
	separator := first.start + first.lines
	entry, line, ok := m.window.entryAt(separator)
	if !ok {
		t.Fatal("a row between blocks named no position")
	}
	if entry.block != 1 || line != 0 {
		t.Fatalf("separator row snapped to block %d line %d, want the next block", entry.block, line)
	}
}

func TestSystemClipboardWritesTextWithThePlatformTool(t *testing.T) {
	commands := map[string][]clipboardCommand{
		"darwin":  {{name: "pbcopy"}},
		"windows": {{name: "clip"}},
	}
	for goos, want := range commands {
		c := &systemClipboard{goos: goos}
		got := c.writeCommands()
		if len(got) != 1 || got[0].name != want[0].name {
			t.Fatalf("%s write commands = %+v, want %+v", goos, got, want)
		}
	}
	wayland := &systemClipboard{goos: "linux", getenv: func(key string) string {
		if key == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}}
	if commands := wayland.writeCommands(); len(commands) != 1 || commands[0].name != "wl-copy" {
		t.Fatalf("wayland write commands = %+v, want wl-copy", commands)
	}
	x11 := &systemClipboard{goos: "linux", getenv: func(string) string { return "" }}
	if commands := x11.writeCommands(); len(commands) != 1 || commands[0].name != "xclip" {
		t.Fatalf("x11 write commands = %+v, want xclip", commands)
	}
}

func TestSystemClipboardWriteTextReportsWhatTookIt(t *testing.T) {
	var got string
	c := &systemClipboard{
		goos: "linux",
		getenv: func(string) string {
			return ""
		},
		lookPath: func(name string) (string, error) {
			if name == "xclip" {
				return "/usr/bin/xclip", nil
			}
			return "", errors.New("not found")
		},
		write: func(_ context.Context, _ string, _ []string, text string, _ time.Duration) error {
			got = text
			return nil
		},
	}
	tool, err := c.WriteText(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if tool != "xclip" || got != "hello" {
		t.Fatalf("tool = %q, wrote %q", tool, got)
	}
}

func TestSystemClipboardWriteTextUnavailable(t *testing.T) {
	c := &systemClipboard{
		goos:     "linux",
		getenv:   func(string) string { return "" },
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		write:    func(context.Context, string, []string, string, time.Duration) error { return nil },
	}
	if _, err := c.WriteText(context.Background(), "hello"); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("err = %v, want ErrClipboardUnavailable", err)
	}
}

func TestReplacingTheTranscriptDropsTheSelection(t *testing.T) {
	m := selectableChat(t, nil, block{role: "user", text: "hello world"})
	m.selection = &selection{anchor: selectionPoint{block: 0, line: 0}, cursor: selectionPoint{block: 0, line: 0, col: 5}}
	m.selecting = true

	m.replaceBlocks([]block{{role: "user", text: "another session"}})

	if m.selection != nil || m.selecting {
		t.Fatal("a selection outlived the transcript it was made on")
	}
}

func TestBadgeClickStillWinsOverSelection(t *testing.T) {
	m := selectableChat(t, &recordingClipboard{}, block{
		role:          "assistant",
		text:          "here is an image",
		artifactCount: 1,
		artifacts:     []contracts.Artifact{{MediaType: "image/png", Data: []byte("png")}},
	})
	hit, found := m.badgeAt(m.badgeHits[0].start+m.viewport.Style.GetPaddingLeft(), m.areas[1].y+(m.badgeHits[0].row-m.transcriptOffset))
	if !found {
		t.Fatal("the artifact chip is not where the transcript says it is")
	}
	_, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{X: hit.start + m.viewport.Style.GetPaddingLeft(), Y: m.areas[1].y + (hit.row - m.transcriptOffset), Button: tea.MouseLeft}))
	if cmd == nil {
		t.Fatal("clicking an artifact chip did not save it")
	}
	if _, ok := cmd().(artifactSavedMsg); !ok {
		t.Fatal("clicking an artifact chip did not take the save path")
	}
	if m.selecting {
		t.Fatal("clicking an artifact chip started a selection")
	}
}

// TestCopyReleasesTheOldOwnerOnlyAfterTheNewTextLands covers what happens to the
// process the previous copy left owning the clipboard: it is let go once the new
// text is in, and kept when the new text never lands — releasing it first would
// empty the clipboard on a failed copy.
func TestCopyReleasesTheOldOwnerOnlyAfterTheNewTextLands(t *testing.T) {
	for _, tc := range []struct {
		name        string
		clipboard   Clipboard
		wantRelease bool
	}{
		{name: "the new text landed", clipboard: &recordingClipboard{tool: "wl-copy"}, wantRelease: true},
		{name: "the new text never landed", clipboard: &recordingClipboard{err: ErrClipboardUnavailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := selectableChat(t, tc.clipboard, block{role: "user", text: "hello world"})
			released := false
			m.copyCancel = func() { released = true }

			x, y := screenAt(t, m, "world")
			cmd := drag(t, m, x, y, x+5, y)
			if cmd == nil {
				t.Fatal("the release did not copy the selection")
			}
			_, _ = m.Update(cmd())

			if released != tc.wantRelease {
				t.Fatalf("released the previous owner = %v, want %v", released, tc.wantRelease)
			}
		})
	}
}
