package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/store"
)

// eventCmd is what a single uiEvent means to the chat loop.
type eventCmd int

const (
	cmdKey eventCmd = iota // a printable rune
	cmdBackspace
	cmdEnter   // submit the input
	cmdNewline // insert a newline in the input
	cmdQuit
	cmdDelta // a streamed chunk from the model
	cmdDone  // the turn finished
	cmdTick  // spinner animation tick
	cmdScrollUp
	cmdScrollDown
	cmdMouse // a mouse report (button/coords)
	cmdPaste // bracketed paste completed; text lands in the input
	cmdRedraw
)

// uiEvent is either a terminal key, a scroll/mouse action, or a model-stream
// event (cmdDelta/cmdDone).
type uiEvent struct {
	cmd   eventCmd
	key   rune
	page  bool
	btn   int  // mouse button (SGR code); 0=left, 32=drag, 64/65=wheel
	x, y  int  // mouse cell, 1-based
	press bool // mouse press vs release
	kind  contracts.DeltaKind
	text  string
	paste string // pasted text for cmdPaste
	reply *ChatReply
	err   error
}

// block is one rendered conversation entry.
type block struct {
	role string // "user" | "thinking" | "assistant"
	text string
}

// histRow is one rendered, uncolored line of the conversation, tagged with
// its style. It backs both rendering and app-level text selection.
type histRow struct {
	plain string
	style string // "user" | "thinking" | "assistant" | "error"
}

// pos is a cell in the conversation content (row = index into rows).
type pos struct{ row, col int }

// chatTUI is the raw-mode, alternate-screen chat UI: history scrolls on top,
// a status bar sits above a sticky multi-line input at the very bottom.
type chatTUI struct {
	client  Client
	session store.SessionMeta
	used    int

	width, height int
	events        chan uiEvent

	blocks []block
	input  string
	scroll int // history scroll offset from the bottom (0 = newest)
	rows   []histRow

	// app-level text selection (mouse drag), in content coordinates
	selecting bool
	selStart  pos
	selEnd    pos
	flash     string // transient notice (e.g. clipboard result)

	// bracketed paste state: everything between ESC[200~ .. ESC[201~ is
	// inserted literally (newlines stay as newlines, Enter does not submit).
	pasting  bool
	pasteBuf string

	working   bool
	workBegan time.Time
	done      chan struct{} // closed on exit to stop the tick goroutine
}

// runChatTUI enters raw mode + the alternate screen and runs the chat loop
// until the user quits. seed is the resumed session's history, if any.
func runChatTUI(ctx context.Context, c Client, session store.SessionMeta, used int, seed []block) error {
	t := &chatTUI{
		client:  c,
		session: session,
		used:    used,
		events:  make(chan uiEvent, 256),
		input:   "",
		blocks:  seed,
		done:    make(chan struct{}),
	}
	defer close(t.done)

	fd := os.Stdout.Fd()
	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	defer term.Restore(fd, state)

	w, h, err := term.GetSize(fd)
	if err != nil {
		return fmt.Errorf("get terminal size: %w", err)
	}
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	t.width, t.height = w, h

	// Redraw on terminal resize so the full-width rules always match the
	// current window.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
				t.width, t.height = w, h
			}
			t.events <- uiEvent{cmd: cmdRedraw}
		}
	}()

	// ButtonEvent mouse (1002) + SGR coordinates (1006) + bracketed paste
	// (2004): wheel/click/drag reach us, and pasted text arrives as one
	// literal block (newlines preserved, Enter does not submit mid-paste).
	fmt.Fprint(os.Stdout, "\033[?1049h\033[?1002h\033[?1006h\033[?2004h")
	defer fmt.Fprint(os.Stdout, "\033[?2004l\033[?1002l\033[?1006l\033[?1049l")

	go t.readKeys()
	go t.tick()

	t.redraw()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-t.events:
			switch ev.cmd {
			case cmdQuit:
				return nil
			case cmdKey:
				t.input += string(ev.key)
				t.redraw()
			case cmdBackspace:
				if r, n := utf8.DecodeLastRuneInString(t.input); n > 0 {
					t.input = t.input[:len(t.input)-n]
					_ = r
				}
				t.redraw()
			case cmdNewline:
				t.input += "\n"
				t.redraw()
			case cmdEnter:
				t.submit(ctx)
			case cmdDelta:
				t.scroll = 0
				t.appendDelta(ev.kind, ev.text)
				t.redraw()
			case cmdDone:
				t.working = false
				t.scroll = 0
				if ev.err != nil {
					t.appendError(ev.err)
				} else if ev.reply != nil {
					t.used += tokenCount(ev.reply)
					t.session.TurnCount++
					t.session.Model = ev.reply.Model
				}
				t.redraw()
			case cmdTick:
				// Only redraw on idle ticks while working; redrawing while
				// idle would stomp on mouse text selection.
				if t.working {
					t.redraw()
				}
			case cmdScrollUp:
				t.scrollUp(ev.page)
				t.redraw()
			case cmdScrollDown:
				t.scrollDown(ev.page)
				t.redraw()
			case cmdMouse:
				t.handleMouse(ev)
				t.redraw()
			case cmdPaste:
				t.input += ev.paste
				t.redraw()
			case cmdRedraw:
				t.redraw()
			}
		}
	}
}

// submit sends the current input as a user message and starts a turn.
func (t *chatTUI) submit(ctx context.Context) {
	prompt := strings.TrimSpace(t.input)
	if prompt == "" || t.working {
		return
	}
	t.input = ""
	t.blocks = append(t.blocks, block{role: "user", text: prompt})
	t.scroll = 0
	t.working = true
	t.workBegan = time.Now()

	ev := make(chan uiEvent, 256)
	go func() {
		reply, err := t.client.Chat(ctx, prompt, func(kind contracts.DeltaKind, text string) {
			ev <- uiEvent{cmd: cmdDelta, kind: kind, text: text}
		})
		ev <- uiEvent{cmd: cmdDone, reply: reply, err: err}
		close(ev)
	}()
	go func() {
		for e := range ev {
			t.events <- e
		}
	}()

	t.redraw()
}

func (t *chatTUI) appendDelta(kind contracts.DeltaKind, text string) {
	role := "assistant"
	if kind == contracts.DeltaReasoning {
		role = "thinking"
	}
	if len(t.blocks) > 0 && t.blocks[len(t.blocks)-1].role == role {
		t.blocks[len(t.blocks)-1].text += text
		return
	}
	t.blocks = append(t.blocks, block{role: role, text: text})
}

func (t *chatTUI) appendError(err error) {
	if len(t.blocks) > 0 && t.blocks[len(t.blocks)-1].role == "error" {
		t.blocks[len(t.blocks)-1].text = err.Error()
		return
	}
	t.blocks = append(t.blocks, block{role: "error", text: err.Error()})
}

func tokenCount(r *ChatReply) int {
	if r == nil || r.Usage == nil {
		return 0
	}
	return r.Usage.PromptTokens + r.Usage.CompletionTokens
}

// blocksFromRecords converts a saved transcript into chat blocks so a resumed
// session renders its history.
func blocksFromRecords(records []store.Record) []block {
	var blocks []block
	for _, rec := range records {
		if rec.Kind != store.KindMessage || rec.Message == nil {
			continue
		}
		m := rec.Message
		switch m.Role {
		case "user":
			blocks = append(blocks, block{role: "user", text: m.Text()})
		case "assistant":
			if m.ReasoningContent != "" {
				blocks = append(blocks, block{role: "thinking", text: m.ReasoningContent})
			}
			blocks = append(blocks, block{role: "assistant", text: m.Text()})
		}
	}
	return blocks
}

// readKeys pushes decoded terminal input onto t.events.
func (t *chatTUI) readKeys() {
	r := bufio.NewReader(os.Stdin)
	for {
		c, err := r.ReadByte()
		if err != nil {
			t.events <- uiEvent{cmd: cmdQuit}
			return
		}

		// While bracketed paste is active, everything is literal: newlines and
		// Enter stay as text, only ESC[201~ ends the paste. UTF-8 is decoded
		// rune by rune so multi-byte characters of any width survive intact.
		if t.pasting {
			if c == 0x1b {
				if next, err := r.Peek(5); err == nil && string(next) == "[201~" {
					_, _ = r.Discard(5)
					t.pasting = false
					t.events <- uiEvent{cmd: cmdPaste, paste: t.pasteBuf}
					t.pasteBuf = ""
					continue
				}
			}
			seq := []byte{c}
			for !utf8.FullRune(seq) {
				b, err := r.ReadByte()
				if err != nil {
					break
				}
				if b == 0x1b { // paste-end starting mid-rune; handle next loop
					break
				}
				seq = append(seq, b)
			}
			if run, _ := utf8.DecodeRune(seq); run != utf8.RuneError {
				t.pasteBuf += string(run)
			}
			continue
		}

		switch c {
		case 0x1b: // escape sequence, Alt+Enter, or a mouse report
			next, err := r.Peek(1)
			if err != nil {
				continue
			}
			switch next[0] {
			case '[': // CSI or X10/SGR mouse
				_, _ = r.Discard(1) // consume '['
				if b, err := r.Peek(1); err == nil {
					switch b[0] {
					case '<': // SGR mouse report: ESC [ < btn ; x ; y M|m
						_, _ = r.Discard(1)
						if btn, x, y, press := sgrMouse(r); btn >= 0 {
							t.events <- t.mouseEvent(btn, x, y, press)
						}
						continue
					case 'M': // X10 mouse report: ESC [ M <btn+32> <x+32> <y+32>
						_, _ = r.Discard(1)
						if b1, b2, b3, err := read3(r); err == nil {
							t.events <- t.mouseEvent(int(b1)-32, int(b2)-32, int(b3)-32, true)
						}
						continue
					}
				}
				var seq []byte
				for {
					b, err := r.ReadByte()
					if err != nil {
						break
					}
					seq = append(seq, b)
					if b >= 0x40 && b <= 0x7e {
						break
					}
				}
				if string(seq) == "200~" {
					t.pasting = true
					t.pasteBuf = ""
					continue
				}
				t.handleCSI(seq)
			case '<': // SGR mouse report (some terminals skip the '[')
				_, _ = r.Discard(1)
				if btn, x, y, press := sgrMouse(r); btn >= 0 {
					t.events <- t.mouseEvent(btn, x, y, press)
				}
			case 'M': // X10 mouse report without the '['
				_, _ = r.Discard(1)
				if b1, b2, b3, err := read3(r); err == nil {
					t.events <- t.mouseEvent(int(b1)-32, int(b2)-32, int(b3)-32, true)
				}
			case 0x0d: // Alt+Enter = newline
				_, _ = r.Discard(1)
				t.events <- uiEvent{cmd: cmdNewline}
			}
		case 0x0d:
			t.events <- uiEvent{cmd: cmdEnter}
		case 0x0a: // Ctrl+J = newline
			t.events <- uiEvent{cmd: cmdNewline}
		case 0x7f, 0x08:
			t.events <- uiEvent{cmd: cmdBackspace}
		case 0x03, 0x04:
			t.events <- uiEvent{cmd: cmdQuit}
		default:
			seq := []byte{c}
			for !utf8.FullRune(seq) {
				b, err := r.ReadByte()
				if err != nil {
					break
				}
				seq = append(seq, b)
			}
			run, _ := utf8.DecodeRune(seq)
			t.events <- uiEvent{cmd: cmdKey, key: run}
		}
	}
}

// CopyToClipboard copies text to the system clipboard via the first available
// of wl-copy / xclip / pbcopy.
func CopyToClipboard(text string) (string, error) {
	for _, entry := range []struct {
		bin  string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"pbcopy", nil},
	} {
		path, err := exec.LookPath(entry.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, entry.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return entry.bin, err
		}
		return entry.bin, nil
	}
	return "", nil
}

// sgrMouse reads an SGR mouse report ("ESC[<btn;x;yM") and returns the button
// code, cell coordinates (1-based) and whether it was a press ('M') rather
// than a release ('m'). btn is -1 on a malformed report. Wheel up is 64, wheel
// down is 65, left drag is 32.
func sgrMouse(r *bufio.Reader) (btn, x, y int, press bool) {
	btn, x, y = 0, 0, 0
	press = true
	part := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return -1, 0, 0, false
		}
		switch {
		case b == ';':
			part++
		case b == 'M':
			return btn, x, y, true
		case b == 'm':
			return btn, x, y, false
		case b >= '0' && b <= '9':
			switch part {
			case 0:
				btn = btn*10 + int(b-'0')
			case 1:
				x = x*10 + int(b-'0')
			case 2:
				y = y*10 + int(b-'0')
			}
		}
	}
}

// read3 reads three consecutive bytes (for X10 mouse reports).
func read3(r *bufio.Reader) (byte, byte, byte, error) {
	b1, err := r.ReadByte()
	if err != nil {
		return 0, 0, 0, err
	}
	b2, err := r.ReadByte()
	if err != nil {
		return 0, 0, 0, err
	}
	b3, err := r.ReadByte()
	return b1, b2, b3, err
}

// mouseEvent routes a mouse report to scroll or selection.
func (t *chatTUI) mouseEvent(btn, x, y int, press bool) uiEvent {
	switch {
	case btn == 64: // wheel up
		return uiEvent{cmd: cmdScrollUp}
	case btn == 65: // wheel down
		return uiEvent{cmd: cmdScrollDown}
	}
	// left press/drag (0/32) and everything else feed the selection handler
	return uiEvent{cmd: cmdMouse, btn: btn, x: x, y: y, press: press}
}

// handleMouse updates the app-level selection from a mouse event.
func (t *chatTUI) handleMouse(ev uiEvent) {
	if ev.press {
		if p, ok := t.posFromMouse(ev.x, ev.y); ok {
			if ev.btn == 0 { // left down: start
				t.selecting = true
				t.selStart, t.selEnd = p, p
			} else { // drag: extend
				t.selEnd = p
			}
		} else {
			t.selecting = false
		}
		return
	}
	// release
	if t.selecting {
		t.copySelection()
		t.selecting = false
	}
}

// posFromMouse maps a mouse cell (1-based) to a content position, if it lands
// in the history region.
func (t *chatTUI) posFromMouse(x, y int) (pos, bool) {
	avail := t.historyHeight()
	row := y - 1 // history occupies the top rows
	if row < 0 || row >= avail {
		return pos{}, false
	}
	start := max(len(t.rows)-avail-t.scroll, 0)
	contentRow := start + row
	if contentRow < 0 || contentRow >= len(t.rows) {
		return pos{}, false
	}
	col := max(x-1, 0)
	if n := utf8.RuneCountInString(t.rows[contentRow].plain); col > n {
		col = n
	}
	return pos{contentRow, col}, true
}

// selBounds returns the normalized [start,end] content positions.
func (t *chatTUI) selBounds() (pos, pos) {
	a, b := t.selStart, t.selEnd
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		return b, a
	}
	return a, b
}

// renderRow colors a row's plain text, wrapping any selected columns in
// reverse video.
func (t *chatTUI) renderRow(hr histRow, selected bool, a, b int) string {
	s := hr.plain
	if selected {
		runes := []rune(s)
		lo := clamp(a, 0, len(runes))
		hi := clamp(b, 0, len(runes))
		if lo < hi {
			s = string(runes[:lo]) + "\033[7m" + string(runes[lo:hi]) + "\033[0m" + string(runes[hi:])
		}
	}
	switch hr.style {
	case "user":
		return cUser.Sprint(s)
	case "assistant":
		return cAssistant.Sprint(s)
	case "thinking":
		return cThinking.Sprint(s)
	case "error":
		return cError.Sprint(s)
	}
	return s
}

// copySelection copies the selected text to the clipboard.
func (t *chatTUI) copySelection() {
	if !t.selecting {
		return
	}
	lo, hi := t.selBounds()
	if lo.row >= len(t.rows) {
		return
	}
	var parts []string
	for r := lo.row; r <= hi.row && r < len(t.rows); r++ {
		line := t.rows[r].plain
		if r == lo.row && r == hi.row {
			parts = append(parts, sliceRunes(line, lo.col, hi.col))
		} else if r == lo.row {
			parts = append(parts, sliceRunes(line, lo.col, utf8.RuneCountInString(line)))
		} else if r == hi.row {
			parts = append(parts, sliceRunes(line, 0, hi.col))
		} else {
			parts = append(parts, line)
		}
	}
	if _, err := CopyToClipboard(strings.Join(parts, "\n")); err != nil {
		t.flash = "copy failed: " + err.Error()
	} else {
		t.flash = "copied"
	}
}

func sliceRunes(s string, a, b int) string {
	r := []rune(s)
	if a < 0 {
		a = 0
	}
	if b > len(r) {
		b = len(r)
	}
	if a > b {
		a = b
	}
	return string(r[a:b])
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// handleCSI maps arrow, page and modified-Enter sequences to actions.
func (t *chatTUI) handleCSI(seq []byte) {
	if len(seq) == 0 {
		return
	}
	if modifiedEnter(seq) {
		t.events <- uiEvent{cmd: cmdNewline}
		return
	}
	switch seq[len(seq)-1] {
	case 'A':
		t.events <- uiEvent{cmd: cmdScrollUp}
	case 'B':
		t.events <- uiEvent{cmd: cmdScrollDown}
	case '~':
		if len(seq) >= 2 {
			switch seq[0] {
			case '5':
				t.events <- uiEvent{cmd: cmdScrollUp, page: true}
			case '6':
				t.events <- uiEvent{cmd: cmdScrollDown, page: true}
			}
		}
	}
}

// modifiedEnter reports whether a CSI sequence is Enter with a modifier, which
// terminals send for Shift+Enter / Alt+Enter instead of a raw "\x1b\r". These
// are the newline-in-input key in Kitty ("13;2u"/"13;3u") and xterm
// modifyOtherKeys ("27;2;13~"/"27;3;13~").
func modifiedEnter(seq []byte) bool {
	s := string(seq)
	var key, mod int
	if strings.HasSuffix(s, "u") {
		parts := strings.Split(strings.TrimSuffix(s, "u"), ";")
		if len(parts) < 2 {
			return false
		}
		_, _ = fmt.Sscanf(parts[0], "%d", &key)
		_, _ = fmt.Sscanf(parts[1], "%d", &mod)
	} else if strings.HasSuffix(s, "~") && strings.HasPrefix(s, "27;") {
		parts := strings.Split(strings.TrimSuffix(s, "~"), ";")
		if len(parts) < 3 {
			return false
		}
		_, _ = fmt.Sscanf(parts[1], "%d", &mod)
		_, _ = fmt.Sscanf(parts[2], "%d", &key)
	}
	return key == 13 && (mod == 2 || mod == 3 || mod == 5)
}

// historyHeight is how many terminal rows the history region occupies.
func (t *chatTUI) historyHeight() int {
	avail := t.height - t.inputAreaRows() - t.statusRows() - t.brandRows()
	if avail < 0 {
		return 0
	}
	return avail
}

func (t *chatTUI) maxScroll() int {
	avail := t.historyHeight()
	h := len(t.buildRows())
	if h <= avail {
		return 0
	}
	return h - avail
}

func (t *chatTUI) scrollUp(page bool) {
	step := 3 // wheel feels like a few lines per tick
	if page {
		step = t.historyHeight()
	}
	t.selecting = false
	t.scroll += step
	if t.scroll > t.maxScroll() {
		t.scroll = t.maxScroll()
	}
}

func (t *chatTUI) scrollDown(page bool) {
	step := 3
	if page {
		step = t.historyHeight()
	}
	t.selecting = false
	t.scroll -= step
	if t.scroll < 0 {
		t.scroll = 0
	}
}

// tick drives the spinner animation while working, and stops on exit.
func (t *chatTUI) tick() {
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tk.C:
			t.events <- uiEvent{cmd: cmdTick}
		}
	}
}

// ---- rendering ----

func (t *chatTUI) redraw() {
	var sb strings.Builder
	sb.WriteString("\033[H")

	// conversation history: exactly `avail` rows starting at `start`, so
	// scrolling reveals earlier rows and hides the bottom ones.
	t.rows = t.buildRows()
	avail := t.historyHeight()
	if t.scroll > t.maxScroll() {
		t.scroll = t.maxScroll()
	}
	start := max(len(t.rows)-avail-t.scroll, 0)
	lo, hi := pos{}, pos{}
	hasSel := t.selecting
	if hasSel {
		lo, hi = t.selBounds()
	}
	for i := range avail {
		r := start + i
		if r >= len(t.rows) {
			sb.WriteString("\033[K\r\n")
			continue
		}
		sel := false
		a, b := 0, 0
		if hasSel && r >= lo.row && r <= hi.row {
			sel = true
			a = lo.col
			b = hi.col
			if r != lo.row {
				a = 0
			}
			if r != hi.row {
				b = utf8.RuneCountInString(t.rows[r].plain)
			}
		}
		sb.WriteString(t.renderRow(t.rows[r], sel, a, b))
		sb.WriteString("\033[K\r\n")
	}

	// status bar
	status := t.statusLine()
	if t.flash != "" {
		status = cSystem.Sprintf("%s · %s", status, t.flash)
		t.flash = ""
	}
	sb.WriteString(status)
	sb.WriteString("\033[K\r\n")

	// input (sticky at the very bottom), framed by top and bottom rules, with
	// the "infai harness" brand line beneath it.
	sb.WriteString(cHeader.Sprint(strings.Repeat("─", t.width)))
	sb.WriteString("\033[K\r\n")
	lines := strings.Split(t.input, "\n")
	for i, l := range lines {
		if i == len(lines)-1 {
			sb.WriteString(cPrompt.Sprintf("λ %s", l))
			sb.WriteString("\033[K")
		} else {
			sb.WriteString(l)
			sb.WriteString("\033[K\r\n")
		}
	}
	sb.WriteString("\r\n")
	sb.WriteString(cHeader.Sprint(strings.Repeat("─", t.width)))
	sb.WriteString("\033[K\r\n")
	sb.WriteString(cHeader.Sprint("infai harness"))
	sb.WriteString("\033[K")
	// place the blinking cursor back on the input's last line at its column
	sb.WriteString(fmt.Sprintf("\033[2A\r\033[%dC", utf8.RuneCountInString(lines[len(lines)-1])+2))

	_, _ = io.WriteString(os.Stdout, sb.String())
}

// inputRows is how many terminal rows the input text occupies.
func (t *chatTUI) inputRows() int {
	return len(strings.Split(t.input, "\n"))
}

// inputAreaRows is the input text plus its top/bottom border lines.
func (t *chatTUI) inputAreaRows() int {
	return t.inputRows() + 2
}

// statusRows is the height of the status bar; the working indicator (spinner)
// IS the status line, so it is always one row.
func (t *chatTUI) statusRows() int {
	return 1
}

// brandRows is the "infai harness" line below the input.
func (t *chatTUI) brandRows() int {
	return 1
}

// buildRows renders the conversation as plain (uncolored) wrapped rows tagged
// with their style. Plain text is kept so selection can map cells and extract
// the copied text; coloring happens in renderRow.
func (t *chatTUI) buildRows() []histRow {
	var out []histRow
	for _, b := range t.blocks {
		switch b.role {
		case "user":
			for i, l := range wrap(b.text, t.width-3) {
				prefix := "● "
				if i > 0 {
					prefix = "  "
				}
				out = append(out, histRow{plain: prefix + l, style: "user"})
			}
		case "assistant":
			for i, l := range wrap(b.text, t.width-3) {
				prefix := "● "
				if i > 0 {
					prefix = "  "
				}
				out = append(out, histRow{plain: prefix + l, style: "assistant"})
			}
		case "thinking":
			out = append(out, histRow{plain: "─ thinking ─", style: "thinking"})
			for _, l := range wrap(b.text, t.width) {
				out = append(out, histRow{plain: l, style: "thinking"})
			}
		case "error":
			for _, l := range wrap(b.text, t.width-3) {
				out = append(out, histRow{plain: "! " + l, style: "error"})
			}
		}
	}
	return out
}

// spinnerFrame returns the current spinner glyph, driven by elapsed time.
func (t *chatTUI) spinnerFrame() string {
	frames := []string{"◐", "◓", "◑", "◒"}
	return frames[time.Since(t.workBegan).Milliseconds()/200%int64(len(frames))]
}

// statusLine builds the model/session/context status bar.
func (t *chatTUI) statusLine() string {
	scrolled := ""
	if t.scroll > 0 {
		scrolled = fmt.Sprintf("  ·  ↑ %d", t.scroll)
	}
	if t.working {
		pct := 0
		if t.session.ContextWindow > 0 {
			pct = t.used * 100 / t.session.ContextWindow
		}
		return cThinking.Sprintf("%s %s · %s · ctx: %d/%d (%d%%)",
			t.spinnerFrame(), t.session.Model, time.Since(t.workBegan).Round(time.Second), t.used, t.session.ContextWindow, pct)
	}
	pct := 0
	if t.session.ContextWindow > 0 {
		pct = t.used * 100 / t.session.ContextWindow
	}
	return cHeader.Sprintf("model: %s  ·  ctx: %d/%d (%d%%)  ·  sess: %s  ·  turns: %d%s",
		t.session.Model, t.used, t.session.ContextWindow, pct, shortID(t.session.ID), t.session.TurnCount, scrolled)
}

// wrap splits long lines to at most w runes (char-based; colored per line by
// the caller).
func wrap(s string, w int) []string {
	if w <= 0 {
		w = 80
	}
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		runes := []rune(line)
		for len(runes) > w {
			out = append(out, string(runes[:w]))
			runes = runes[w:]
		}
		out = append(out, string(runes))
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
