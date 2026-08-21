//go:build linux || darwin

package main

// The REPL's terminal layer, hand-rolled on x/sys termios (already a project
// dependency): a raw-mode line editor with Tab completion, and an interrupt
// watcher (Esc / Ctrl-C) while the ask engine runs. Deliberately small — the
// codex-style basics, not a full TUI framework; keeping it in-repo avoids a
// Bubble Tea dependency tree for what is a prompt + status line.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// termSession owns one terminal's raw-mode state for the REPL.
type termSession struct {
	fd          int
	old         *unix.Termios
	history     []string // oldest first, capped
	historyPath string   // "" = no persistence
	notifyCh    chan string
}

// initHistory loads previously entered lines from path (created on demand,
// 0600: questions may carry private context) and marks it for persistence.
func (t *termSession) initHistory(path string) {
	t.historyPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			t.history = append(t.history, l)
		}
	}
	if n := len(t.history); n > 1000 {
		t.history = t.history[n-1000:]
	}
}

// recordHistory appends line (deduping consecutive repeats), keeps the cap,
// and rewrites the file — a rewrite of ≤1000 short lines per submit is
// cheaper than tracking appends.
func (t *termSession) recordHistory(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if n := len(t.history); n > 0 && t.history[n-1] == line {
		return
	}
	t.history = append(t.history, line)
	if n := len(t.history); n > 1000 {
		t.history = t.history[n-1000:]
	}
	if t.historyPath == "" {
		return
	}
	_ = os.WriteFile(t.historyPath, []byte(strings.Join(t.history, "\n")+"\n"), 0o600)
}

// runeWidth approximates the terminal column count of r: East Asian and
// emoji ranges render double-wide, combining marks zero. A compact table —
// full wcwidth tables are overkill for a line editor.
func runeWidth(r rune) int {
	if r < 0x20 {
		return 0
	}
	if r >= 0x0300 && r <= 0x036F { // combining diacritics
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals · Kangxi · CJK symbols
		r >= 0x3041 && r <= 0x33FF, // kana · CJK strokes · compat
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xA000 && r <= 0xA4CF, // Yi · Hangul syllable blocks start
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compat ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compat forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD: // CJK ext B+
		return 2
	}
	return 1
}

// displayWidth sums the column count of s.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// newTermSession returns a session when stdin is a real terminal.
func newTermSession() *termSession {
	if !stdinIsTTY() {
		return nil
	}
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil
	}
	return &termSession{fd: fd, old: old, notifyCh: make(chan string, 16)}
}

// deliver queues one background notification (a task finished) for the
// line editor to print between keystrokes. It reports whether the message
// was queued; a full queue drops the line rather than blocking the caller.
func (t *termSession) deliver(line string) bool {
	if t == nil || t.notifyCh == nil {
		return false
	}
	select {
	case t.notifyCh <- line:
		return true
	default:
		return false
	}
}

func (t *termSession) setRaw(peek bool) error {
	raw := *t.old
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.ISTRIP | unix.INLCR | unix.ICRNL | unix.IGNCR | unix.IXON | unix.IXOFF
	if peek {
		// Non-blocking reads (Esc-sequence peeking).
		raw.Cc[unix.VMIN] = 0
		raw.Cc[unix.VTIME] = 2 // 200ms
	} else {
		raw.Cc[unix.VMIN] = 1
		raw.Cc[unix.VTIME] = 0
	}
	return unix.IoctlSetTermios(t.fd, ioctlWriteTermios, &raw)
}

func (t *termSession) restore() {
	if t.old != nil {
		_ = unix.IoctlSetTermios(t.fd, ioctlWriteTermios, t.old)
	}
}

func (t *termSession) readByte() (byte, error) {
	var buf [1]byte
	n, err := unix.Read(t.fd, buf[:])
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil // VTIME timeout (peek mode)
	}
	return buf[0], nil
}

// readLine is the raw-mode line editor: UTF-8-aware insertion anywhere in
// the line (Left/Right/Home/End/Ctrl-A/E/B/F), backspace and forward delete
// (Del, Ctrl-D), word/line kills (Ctrl-W, Ctrl-K, Ctrl-U), history recall
// (Up/Down, persisted across runs), and Tab completion over the word list.
//
// It runs in peek mode (VTIME poll) so background notifications — a task
// finishing elsewhere — interleave safely: the current render is cleared,
// the notice prints on its own line, and the prompt+buffer redraw intact.
//
// Rendering is wrap-aware: the prompt+buffer may span several physical rows
// on narrow terminals or long CJK input, and the slash menu sits above it
// refreshing IN PLACE (ANSI cursor-up + erase-below, never scroll-append).
// The menu never rewrites the buffer — completion is Tab's job. A lone
// Ctrl-C clears the line; a second within one second exits (double-tap).
func (t *termSession) readLine(prompt string, completions []string) (string, error) {
	if err := t.setRaw(true); err != nil {
		return "", err
	}
	defer t.restore()
	fmt.Print(prompt)

	var buf []rune
	pos := 0                  // cursor index within buf
	prevRows := 1             // physical rows the previous render occupies
	histIdx := len(t.history) // one past the newest entry = the live draft
	draft := ""
	lastCtrlC := time.Time{}

	promptW := displayWidth(prompt)
	dim := func(s string) string {
		if !stdoutIsTTY() {
			return s
		}
		return "\x1b[2m" + s + "\x1b[0m"
	}
	termW := func() int {
		if w := t.width(); w >= 20 {
			return w
		}
		return 80
	}
	// clearView erases the whole previous render (menu rows + wrapped buffer
	// rows) in one shot: cursor up to the top row, then erase-to-end-of-screen.
	clearView := func() {
		if prevRows > 1 {
			fmt.Printf("\x1b[%dA", prevRows-1)
		}
		fmt.Print("\r\x1b[J")
		prevRows = 0
	}
	// menuText is the single-line candidate list for the current buffer, or
	// "" when there is nothing to suggest.
	menuText := func() string {
		line := string(buf)
		if !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
			return ""
		}
		var matches []string
		for _, c := range completions {
			if strings.HasPrefix(c, line) {
				matches = append(matches, c)
			}
		}
		sort.Strings(matches)
		if len(matches) == 0 || (len(matches) == 1 && matches[0] == line) {
			return ""
		}
		const maxShow = 10
		shown, tail := matches, ""
		if len(matches) > maxShow {
			shown = matches[:maxShow]
			tail = fmt.Sprintf("  (+%d more, Tab)", len(matches)-maxShow)
		}
		text := "  " + strings.Join(shown, "   ") + tail
		if w := termW(); displayWidth(text) > w-1 {
			if r := []rune(text); len(r) > w-1 {
				text = string(r[:w-2]) + ">"
			}
		}
		return text
	}
	// redraw is the single paint path: clear, then menu row (if any), then
	// prompt+buffer, then park the cursor at the edit position.
	redraw := func() {
		clearView()
		rows := 0
		if m := menuText(); m != "" {
			fmt.Print(dim(m) + "\r\n")
			rows++
		}
		w := termW()
		total := promptW + displayWidth(string(buf))
		lineRows := (total + w - 1) / w
		if lineRows < 1 {
			lineRows = 1
		}
		fmt.Print(prompt + string(buf))
		// Cursor: after the full print it sits at the text end (row
		// lineRows-1, possibly in pending-wrap). Move it back to pos unless
		// pos is already the end.
		curCols := promptW + displayWidth(string(buf[:pos]))
		if curCols != total {
			curRow, curCol := curCols/w, curCols%w
			if up := lineRows - 1 - curRow; up > 0 {
				fmt.Printf("\x1b[%dA", up)
			}
			fmt.Print("\r")
			if curCol > 0 {
				fmt.Printf("\x1b[%dC", curCol)
			}
		}
		prevRows = rows + lineRows
	}
	histSet := func(s string) {
		buf = []rune(s)
		pos = len(buf)
		redraw()
	}
	histPrev := func() {
		if histIdx > 0 {
			if histIdx == len(t.history) {
				draft = string(buf)
			}
			histIdx--
			histSet(t.history[histIdx])
		}
	}
	histNext := func() {
		if histIdx < len(t.history) {
			histIdx++
			if histIdx == len(t.history) {
				histSet(draft)
			} else {
				histSet(t.history[histIdx])
			}
		}
	}
	insert := func(r rune) {
		buf = append(buf, 0)
		copy(buf[pos+1:], buf[pos:])
		buf[pos] = r
		pos++
		redraw()
	}
	backspace := func() {
		if pos > 0 {
			buf = append(buf[:pos-1], buf[pos:]...)
			pos--
			redraw()
		}
	}
	deleteForward := func() {
		if pos < len(buf) {
			buf = append(buf[:pos], buf[pos+1:]...)
			redraw()
		}
	}

	for {
		b, err := t.readByte()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return "", io.EOF
			}
			// EINTR: retry.
			if errno, ok := err.(unix.Errno); ok && errno == unix.EINTR {
				continue
			}
			return "", err
		}
		if b == 0 {
			// Peek timeout: drain one pending background notification, if
			// any, then return to waiting. clearView erases the current
			// render (menu rows + wrapped prompt), the notice lands on its
			// own line, and redraw repaints the editing state below it.
			select {
			case line := <-t.notifyCh:
				clearView()
				fmt.Print(line + "\r\n")
				prevRows = 0
				redraw()
			default:
			}
			continue
		}
		switch b {
		case 0x0d, 0x0a: // Enter: erase the menu/wrap rows, echo, submit
			line := string(buf)
			clearView()
			fmt.Print(prompt + line + "\r\n")
			t.recordHistory(line)
			return line, nil
		case 0x03: // Ctrl-C: clear line; double-tap exits
			if !lastCtrlC.IsZero() && time.Since(lastCtrlC) < time.Second {
				clearView()
				fmt.Print("\r\n")
				os.Exit(130)
			}
			lastCtrlC = time.Now()
			buf, pos, histIdx, draft = nil, 0, len(t.history), ""
			clearView()
			fmt.Print("^C\r\n" + prompt)
			prevRows = 1
		case 0x04: // Ctrl-D: EOF when empty, forward delete otherwise
			if len(buf) == 0 {
				clearView()
				fmt.Print("\r\n")
				return "", io.EOF
			}
			deleteForward()
		case 0x01: // Ctrl-A: home
			pos = 0
			redraw()
		case 0x05: // Ctrl-E: end
			pos = len(buf)
			redraw()
		case 0x02: // Ctrl-B: left
			if pos > 0 {
				pos--
				redraw()
			}
		case 0x06: // Ctrl-F: right
			if pos < len(buf) {
				pos++
				redraw()
			}
		case 0x15: // Ctrl-U: kill to start of line
			buf, pos = buf[:0], 0
			redraw()
		case 0x0b: // Ctrl-K: kill to end of line
			buf = buf[:pos]
			redraw()
		case 0x17: // Ctrl-W: kill the word before the cursor
			if pos > 0 {
				j := pos
				for j > 0 && buf[j-1] == ' ' {
					j--
				}
				for j > 0 && buf[j-1] != ' ' {
					j--
				}
				buf = append(buf[:j], buf[pos:]...)
				pos = j
				redraw()
			}
		case 0x7f, 0x08: // Backspace
			backspace()
		case 0x09: // Tab: complete the leading slash command
			var completed bool
			buf, completed = tabComplete(buf, completions)
			if completed {
				pos = len(buf)
			}
			redraw()
		case 0x1b: // Esc or an escape sequence (arrows, Home/End, Del)
			seq, isSeq := t.readEscapeSequence()
			if !isSeq {
				continue // a lone Esc is ignored at the prompt
			}
			body := strings.TrimPrefix(strings.TrimPrefix(seq, "["), "O")
			switch body {
			case "A": // Up
				histPrev()
			case "B": // Down
				histNext()
			case "D": // Left
				if pos > 0 {
					pos--
					redraw()
				}
			case "C": // Right
				if pos < len(buf) {
					pos++
					redraw()
				}
			case "H", "1~", "7~": // Home
				pos = 0
				redraw()
			case "F", "4~", "8~": // End
				pos = len(buf)
				redraw()
			case "3~": // Delete (forward)
				deleteForward()
			}
		default:
			if b < 0x20 {
				continue // other control chars: ignore
			}
			// Assemble one UTF-8 rune (multi-byte sequences arrive split).
			r, extra := decodeRune(b, t)
			_ = extra
			insert(r)
		}
	}
}

// width returns the terminal column count (0 when it cannot be queried), so
// the slash menu and wrap math can size themselves to the real screen.
func (t *termSession) width() int {
	ws, err := unix.IoctlGetWinsize(t.fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col < 20 {
		return 0
	}
	return int(ws.Col)
}

// decodeRune reads the continuation bytes of one UTF-8 sequence starting
// with b and returns the rune.
func decodeRune(b byte, t *termSession) (rune, int) {
	size := 0
	switch {
	case b < 0x80:
		return rune(b), 0
	case b&0xe0 == 0xc0:
		size = 1
	case b&0xf0 == 0xe0:
		size = 2
	case b&0xf8 == 0xf0:
		size = 3
	default:
		return 0xfffd, 0
	}
	bytes := []byte{b}
	for i := 0; i < size; i++ {
		nb, err := t.readByte()
		if err != nil {
			return 0xfffd, 0
		}
		bytes = append(bytes, nb)
	}
	runes := []rune(string(bytes))
	if len(runes) == 0 {
		return 0xfffd, 0
	}
	return runes[0], size
}

// readEscapeSequence consumes an ESC-initiated sequence; it returns
// (seq, true) when it was a multi-byte sequence and ("", false) for a lone
// ESC (nothing followed within the peek window).
func (t *termSession) readEscapeSequence() (string, bool) {
	if err := t.setRaw(true); err != nil {
		return "", false
	}
	defer func() { _ = t.setRaw(false) }()
	b, err := t.readByte()
	if err != nil || b == 0 {
		return "", false
	}
	if b != '[' && b != 'O' {
		return string(rune(b)), true
	}
	// CSI: consume until a terminator byte.
	var sb strings.Builder
	sb.WriteByte(b)
	for i := 0; i < 8; i++ {
		nb, err := t.readByte()
		if err != nil || nb == 0 {
			break
		}
		sb.WriteByte(nb)
		if nb >= 0x40 && nb <= 0x7e {
			break
		}
	}
	return sb.String(), true
}

// tabComplete completes the buffer against the candidate list. One match
// completes in place; several complete the common prefix (if longer than the
// line). It never prints — the readLine menu already shows the candidates,
// and stray prints would desync its row accounting.
func tabComplete(buf []rune, candidates []string) ([]rune, bool) {
	line := string(buf)
	if !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
		return buf, false
	}
	var matches []string
	for _, c := range candidates {
		if strings.HasPrefix(c, line) {
			matches = append(matches, c)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return buf, false
	case 1:
		return []rune(matches[0] + " "), true
	default:
		if allSamePrefix(buf, matches) {
			// Complete the common prefix.
			prefix := commonPrefix(matches)
			if len(prefix) > len(line) {
				return []rune(prefix), true
			}
		}
		return buf, false
	}
}

func allSamePrefix(buf []rune, matches []string) bool {
	return len(matches) > 1
}

func commonPrefix(matches []string) string {
	prefix := matches[0]
	for _, m := range matches[1:] {
		for !strings.HasPrefix(m, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// watchInterrupt monitors Esc / Ctrl-C while a long-running ask executes:
// the first press cancels the running context, a second within one second
// exits the process (double-tap). The terminal stays in non-blocking peek
// mode so the watcher never parks in read(2) past ctx completion. Runs until
// ctx completes, then restores cooked mode.
func (t *termSession) watchInterrupt(ctx context.Context, cancel context.CancelFunc, hint string) {
	if t == nil {
		return
	}
	if err := t.setRaw(true); err != nil {
		return
	}
	var first time.Time
	for {
		select {
		case <-ctx.Done():
			t.restore()
			return
		default:
		}
		b, err := t.readByte()
		if err != nil {
			if errno, ok := err.(unix.Errno); ok && errno == unix.EINTR {
				continue
			}
			t.restore()
			return
		}
		if b == 0 {
			continue // peek timeout: loop and re-check ctx
		}
		switch b {
		case 0x1b, 0x03: // Esc or Ctrl-C
			if b == 0x1b {
				// Swallow CSI tails so a stray arrow key never cancels: only
				// a LONE Esc (nothing followed) cancels. readEscapeSequence
				// returns isSeq=true for multi-byte sequences — the old code
				// inverted this, so arrows cancelled and a lone Esc didn't.
				if _, isSeq := t.readEscapeSequence(); isSeq {
					continue
				}
			}
			if !first.IsZero() && time.Since(first) < time.Second {
				t.restore()
				os.Exit(130)
			}
			first = time.Now()
			if hint != "" {
				fmt.Print("\r\n" + hint + "\r\n")
			}
			cancel()
		}
	}
}
