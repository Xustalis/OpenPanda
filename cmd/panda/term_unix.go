//go:build linux || darwin

package main

// The REPL's terminal layer, hand-rolled on x/sys termios (already a project
// dependency): a raw-mode line editor with Tab completion, and an interrupt
// watcher (Esc / Ctrl-C) while the ask engine runs. Deliberately small — the
// codex-style basics, not a full TUI framework; keeping it in-repo avoids a
// Bubble Tea dependency tree for what is a prompt + status line.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// Bracketed paste (DEC 2004): with it on, the terminal wraps pasted text in
// pasteBegin … pasteEnd, which is the only way to tell a pasted newline (part
// of the prompt) from a typed one (submit). Without it a twelve-line paste
// fires twelve asks — every intermediate line dispatched as its own question.
const (
	pasteOn    = "\x1b[?2004h"
	pasteOff   = "\x1b[?2004l"
	pasteBegin = "200~"      // body of ESC[200~, as readEscapeSequence reports it
	pasteEnd   = "\x1b[201~" // matched raw against the byte stream
	maxPaste   = 1 << 20     // 1 MiB: a prompt, not a file transfer
	maxHistory = 1000        // entries kept in memory and on disk
	pasteTab   = "    "      // pasted tabs, widened (see pastedRunes)
)

// termSession owns one terminal's raw-mode state for the REPL.
type termSession struct {
	fd          int
	old         *unix.Termios
	history     []string // oldest first, capped
	historyPath string   // "" = no persistence
	notifyCh    chan string
	// loc is the locale the editor labels its own UI with (the Ctrl-R search
	// line). The REPL owns the locale and /lang can change it mid-session, so
	// it is a plain field the REPL keeps in sync rather than a constructor arg.
	loc i18n.Locale
	// argHint resolves argument-position completions (task ids, session ids,
	// locale codes, config enums). The editor knows the syntax of a command
	// line; only the REPL knows what the arguments mean, so it injects this.
	// nil disables argument completion and leaves command completion intact.
	argHint argResolver
}

// initHistory loads previously entered lines from path (created on demand,
// 0600: questions may carry private context) and marks it for persistence.
func (t *termSession) initHistory(path string) {
	t.historyPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	t.history = loadHistoryFile(data, maxHistory)
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
	if n := len(t.history); n > maxHistory {
		t.history = t.history[n-maxHistory:]
	}
	if t.historyPath == "" {
		return
	}
	_ = os.WriteFile(t.historyPath, encodeHistoryFile(t.history), 0o600)
}

// runeWidth approximates the terminal column count of r. The table lives in
// internal/cliui (the status line needs the same numbers); this stays as the
// editor's local name for it.
func runeWidth(r rune) int { return cliui.RuneWidth(r) }

// displayWidth sums the column count of s.
func displayWidth(s string) int { return cliui.DisplayWidth(s) }

// ---- multi-line buffer geometry -------------------------------------------
//
// The edit buffer is one flat []rune that may contain newlines. Every position
// query below works off that single representation rather than a slice of
// lines, so insertion and deletion stay the plain splice they always were.

// splitLogicalLines cuts buf on newlines. The result always has at least one
// element (an empty buffer is one empty line), and a trailing newline yields a
// trailing empty line — that blank line is real, the cursor can sit on it.
func splitLogicalLines(buf []rune) [][]rune {
	out := make([][]rune, 0, 4)
	start := 0
	for i, r := range buf {
		if r == '\n' {
			out = append(out, buf[start:i])
			start = i + 1
		}
	}
	return append(out, buf[start:])
}

// lineStartAt returns the buffer index that begins the logical line holding i.
func lineStartAt(buf []rune, i int) int {
	if i > len(buf) {
		i = len(buf)
	}
	for j := i - 1; j >= 0; j-- {
		if buf[j] == '\n' {
			return j + 1
		}
	}
	return 0
}

// lineEndAt returns the buffer index just past the logical line holding i
// (the index of its newline, or len(buf) on the last line).
func lineEndAt(buf []rune, i int) int {
	for j := i; j < len(buf); j++ {
		if buf[j] == '\n' {
			return j
		}
	}
	return len(buf)
}

// continuationPrefix is the marker drawn in front of every logical line after
// the first. It is padded to the prompt's width so wrapped rows line up, and
// so the paste of a code block keeps its own indentation readable.
func continuationPrefix(promptW int) string {
	mark := "…"
	if !termSupportsUnicode() || isLinuxConsole() {
		mark = "."
	}
	for displayWidth(mark) < promptW {
		mark += " "
	}
	return mark
}

// pastedRunes normalizes a bracketed-paste payload into buffer content: line
// endings collapse to \n, tabs widen to spaces (runeWidth counts a tab as zero
// columns, so a literal one would desync the wrap math), and every other
// control byte is dropped — a pasted terminal dump can carry escape sequences,
// and re-emitting those would let pasted text repaint the screen. Trailing
// blank lines go: a paste fills the prompt, it does not submit it.
func pastedRunes(s string) []rune {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimRight(s, "\n")
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			out = append(out, r)
		case r == '\t':
			out = append(out, []rune(pasteTab)...)
		case r < 0x20, r == 0x7f:
			// drop
		default:
			out = append(out, r)
		}
	}
	return out
}

// readPaste consumes a bracketed-paste payload: every byte up to the ESC[201~
// terminator. A terminal that never sends one (or a paste larger than the cap)
// ends the read instead of parking the editor forever — the bytes already read
// still land in the buffer, so a truncated paste is visible rather than silent.
func (t *termSession) readPaste() string {
	term := []byte(pasteEnd)
	out := make([]byte, 0, 512)
	idle := 0
	for len(out) < maxPaste {
		b, err := t.readByte()
		if err != nil {
			break
		}
		if b == 0 { // VTIME timeout (peek mode): ~200ms of silence
			idle++
			if idle > 25 {
				break
			}
			continue
		}
		idle = 0
		out = append(out, b)
		if bytes.HasSuffix(out, term) {
			return string(out[:len(out)-len(term)])
		}
	}
	return string(out)
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
// The buffer is multi-line. Ctrl-J (or Alt-Enter) opens a new line, a trailing
// backslash continues onto one, Up/Down walk between them before falling
// through to history, Home/End and the line kills act on the line the cursor
// is on, and a bracketed paste of N lines lands as ONE prompt. Enter is CR
// (0x0d) and only CR: ICRNL/INLCR/IGNCR are all cleared in setRaw, so the
// kernel translates nothing and LF (0x0a) is unambiguously Ctrl-J.
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
	fmt.Print(pasteOn)
	defer fmt.Print(pasteOff)
	fmt.Print(prompt)

	var buf []rune
	pos := 0                  // cursor index within buf
	curRow := 0               // render row the cursor is parked on (0 = top row)
	histIdx := len(t.history) // one past the newest entry = the live draft
	draft := ""
	lastCtrlC := time.Time{}

	promptW := displayWidth(prompt)
	cont := continuationPrefix(promptW)
	contW := displayWidth(cont)
	dim := pal().Muted
	termW := func() int {
		if w := t.width(); w >= 20 {
			return w
		}
		return 80
	}
	// clearView erases the whole previous render (menu row + wrapped buffer
	// rows) in one shot. It moves up by the row the cursor is actually parked
	// on — which is not always the last row, since the cursor can sit mid-buffer
	// — and then erases to the end of the screen.
	clearView := func() {
		if curRow > 0 {
			fmt.Printf("\x1b[%dA", curRow)
		}
		fmt.Print("\r\x1b[J")
		curRow = 0
	}
	// menuText is the single-line candidate list for the current buffer, or
	// "" when there is nothing to suggest.
	menuText := func() string {
		line := string(buf)
		if strings.ContainsRune(line, '\n') {
			return "" // a multi-line draft is prose or a paste, never a command
		}
		if !strings.HasPrefix(line, "/") {
			return ""
		}
		var matches []string
		if strings.Contains(line, " ") {
			// Past the command name: the argument candidates, which only the
			// REPL can resolve (task ids, session ids, config enums).
			_, matches = argCandidatesFor(line, t.argHint)
		} else {
			for _, c := range completions {
				if strings.HasPrefix(c, line) {
					matches = append(matches, c)
				}
			}
			sort.Strings(matches)
			if len(matches) == 1 && matches[0] == line {
				return ""
			}
		}
		if len(matches) == 0 {
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
	// redraw is the single paint path: clear, then the menu row (if any), then
	// every logical line with its prefix, then park the cursor at the edit
	// position. Row bookkeeping is in physical rows throughout, because that is
	// what the cursor-up escapes count.
	redraw := func() {
		clearView()
		top := 0
		if m := menuText(); m != "" {
			fmt.Print(dim(m) + "\r\n")
			top = 1
		}
		w := termW()
		row := top                     // first physical row of the current line
		cursorRow, cursorCol := top, 0 // where pos lands
		endRow, endCol := top, 0       // where the print leaves the cursor
		seen := 0                      // buffer index the current line starts at
		for i, seg := range splitLogicalLines(buf) {
			pfxW := promptW
			if i == 0 {
				fmt.Print(prompt)
			} else {
				fmt.Print("\r\n" + dim(cont))
				pfxW = contW
			}
			fmt.Print(string(seg))
			total := pfxW + displayWidth(string(seg))
			if pos >= seen && pos <= seen+len(seg) {
				c := pfxW + displayWidth(string(seg[:pos-seen]))
				cursorRow, cursorCol = row+c/w, c%w
			}
			// After the print the cursor sits at the text end — on total/w rows
			// down, except at an exact wrap boundary where it stays in pending
			// wrap on the row above (column w).
			if total > 0 {
				endRow, endCol = row+(total-1)/w, (total-1)%w+1
			} else {
				endRow, endCol = row, 0
			}
			rows := (total + w - 1) / w
			if rows < 1 {
				rows = 1
			}
			row += rows
			seen += len(seg) + 1 // +1 for the newline that ended this line
		}
		if pos == len(buf) {
			curRow = endRow // already parked at the end; moving would only risk a wrap
			return
		}
		if cursorRow != endRow || cursorCol != endCol {
			if up := endRow - cursorRow; up > 0 {
				fmt.Printf("\x1b[%dA", up)
			}
			fmt.Print("\r")
			if cursorCol > 0 {
				fmt.Printf("\x1b[%dC", cursorCol)
			}
		}
		curRow = cursorRow
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
	// insertRunes splices rs in at the cursor and repaints ONCE — a pasted
	// block must not trigger one full redraw per character.
	insertRunes := func(rs []rune) {
		if len(rs) == 0 {
			return
		}
		buf = append(buf, rs...)           // grow by len(rs) (values overwritten below)
		copy(buf[pos+len(rs):], buf[pos:]) // shift the tail right (copy is memmove)
		copy(buf[pos:], rs)
		pos += len(rs)
		redraw()
	}
	insert := func(r rune) { insertRunes([]rune{r}) }
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
	// lineUp/lineDown move the cursor between logical lines, keeping the column
	// where it can. They report false when there is no line to move to, which is
	// what makes Up/Down fall through to history on a single-line draft.
	lineUp := func() bool {
		st := lineStartAt(buf, pos)
		if st == 0 {
			return false
		}
		col, prevStart := pos-st, lineStartAt(buf, st-1)
		if np := prevStart + col; np < st-1 {
			pos = np
		} else {
			pos = st - 1
		}
		redraw()
		return true
	}
	lineDown := func() bool {
		en := lineEndAt(buf, pos)
		if en >= len(buf) {
			return false
		}
		col, nextStart := pos-lineStartAt(buf, pos), en+1
		if np := nextStart + col; np < lineEndAt(buf, nextStart) {
			pos = np
		} else {
			pos = lineEndAt(buf, nextStart)
		}
		redraw()
		return true
	}

	// reverseSearch is Ctrl-R: an incremental backwards search over the history,
	// drawn on a single row that replaces the editor's render for the duration.
	// Up/Down already walk the history one entry at a time, which is useless
	// past a dozen entries — this is how a user gets back to the long question
	// they asked yesterday. Enter loads the match into the buffer for editing
	// (it does not submit); Esc or Ctrl-C leaves the draft untouched.
	reverseSearch := func() {
		if len(t.history) == 0 {
			return
		}
		query, match := "", ""
		idx := len(t.history) - 1
		label := i18n.T(t.loc, "repl.search.label")
		// find scans backwards from start for the newest entry containing the
		// query (case-insensitively); -1 means no match at or below start.
		find := func(start int) int {
			q := strings.ToLower(query)
			for i := start; i >= 0; i-- {
				if q == "" || strings.Contains(strings.ToLower(t.history[i]), q) {
					return i
				}
			}
			return -1
		}
		paint := func() {
			prefix := "(" + label + ") " + query + " " + pal().MarkArrow() + " "
			body := match
			if w := termW(); w > 1 {
				if room := w - 1 - displayWidth(prefix); room > 0 {
					body = cliui.Truncate(body, room, pal().Unicode())
				} else {
					body = ""
				}
			}
			fmt.Print("\r\x1b[K" + dim(prefix) + body)
		}
		if i := find(idx); i >= 0 {
			idx, match = i, t.history[i]
		}
		clearView()
		paint()
		for {
			b, err := t.readByte()
			if err != nil {
				break
			}
			if b == 0 {
				continue // peek timeout: keep waiting for a key
			}
			switch b {
			case 0x0d, 0x0a: // Enter: take the match into the draft
				if match != "" {
					buf, pos = []rune(match), len([]rune(match))
					histIdx = len(t.history)
				}
				fmt.Print("\r\x1b[K")
				redraw()
				return
			case 0x03, 0x07: // Ctrl-C / Ctrl-G: abandon the search
				fmt.Print("\r\x1b[K")
				redraw()
				return
			case 0x1b:
				if _, isSeq := t.readEscapeSequence(); !isSeq {
					fmt.Print("\r\x1b[K")
					redraw()
					return
				}
				// An arrow or Home inside the search has no meaning; ignore it.
			case 0x12: // Ctrl-R again: step to the next older match
				if i := find(idx - 1); i >= 0 {
					idx, match = i, t.history[i]
				}
				paint()
			case 0x7f, 0x08: // Backspace: widen the query
				if r := []rune(query); len(r) > 0 {
					query = string(r[:len(r)-1])
					idx = len(t.history) - 1
					if i := find(idx); i >= 0 {
						idx, match = i, t.history[i]
					}
				}
				paint()
			default:
				if b < 0x20 {
					continue
				}
				r, _ := decodeRune(b, t)
				query += string(r)
				if i := find(idx); i >= 0 {
					idx, match = i, t.history[i]
				} else {
					match = "" // no hit: keep the query visible so it can be fixed
				}
				paint()
			}
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
				redraw()
			default:
			}
			continue
		}
		switch b {
		case 0x0d: // Enter: submit, unless a trailing backslash asks for more
			if n := len(buf); n > 0 && buf[n-1] == '\\' {
				buf[n-1] = '\n'
				pos = n
				redraw()
				continue
			}
			line := string(buf)
			clearView()
			echoSubmitted(prompt, dim(cont), line)
			t.recordHistory(line)
			return line, nil
		case 0x0a: // Ctrl-J: open a new line without submitting
			insert('\n')
		case 0x03: // Ctrl-C: clear line; double-tap exits
			if !lastCtrlC.IsZero() && time.Since(lastCtrlC) < time.Second {
				clearView()
				fmt.Print("\r\n")
				fmt.Print(pasteOff)
				os.Exit(130)
			}
			lastCtrlC = time.Now()
			buf, pos, histIdx, draft = nil, 0, len(t.history), ""
			clearView()
			fmt.Print("^C\r\n" + prompt)
		case 0x04: // Ctrl-D: EOF when empty, forward delete otherwise
			if len(buf) == 0 {
				clearView()
				fmt.Print("\r\n")
				return "", io.EOF
			}
			deleteForward()
		case 0x01: // Ctrl-A: start of the current line
			pos = lineStartAt(buf, pos)
			redraw()
		case 0x05: // Ctrl-E: end of the current line
			pos = lineEndAt(buf, pos)
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
		case 0x15: // Ctrl-U: kill to the start of the current line
			if st := lineStartAt(buf, pos); st < pos {
				buf = append(buf[:st], buf[pos:]...)
				pos = st
				redraw()
			}
		case 0x0b: // Ctrl-K: kill to the end of the current line
			if en := lineEndAt(buf, pos); en > pos {
				buf = append(buf[:pos], buf[en:]...)
				redraw()
			}
		case 0x17: // Ctrl-W: kill the word before the cursor
			if pos > 0 {
				j := pos
				for j > 0 && buf[j-1] == ' ' {
					j--
				}
				for j > 0 && buf[j-1] != ' ' && buf[j-1] != '\n' {
					j--
				}
				buf = append(buf[:j], buf[pos:]...)
				pos = j
				redraw()
			}
		case 0x7f, 0x08: // Backspace
			backspace()
		case 0x12: // Ctrl-R: incremental reverse history search
			reverseSearch()
		case 0x09: // Tab: complete the slash command, or its argument
			var completed bool
			buf, completed = tabCompleteAt(buf, completions, t.argHint)
			if completed {
				pos = len(buf)
			}
			redraw()
		case 0x1b: // Esc or an escape sequence (arrows, Home/End, Del, paste)
			seq, isSeq := t.readEscapeSequence()
			if !isSeq {
				continue // a lone Esc is ignored at the prompt
			}
			body := strings.TrimPrefix(strings.TrimPrefix(seq, "["), "O")
			switch body {
			case pasteBegin: // bracketed paste: the payload is data, never keys
				insertRunes(pastedRunes(t.readPaste()))
			case "\r", "\n": // Alt-Enter: newline without submitting
				insert('\n')
			case "A": // Up
				if !lineUp() {
					histPrev()
				}
			case "B": // Down
				if !lineDown() {
					histNext()
				}
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
				pos = lineStartAt(buf, pos)
				redraw()
			case "F", "4~", "8~": // End
				pos = lineEndAt(buf, pos)
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

// echoSubmitted reprints the accepted input above the reply, with the same
// prompt and continuation prefixes the editor drew — so a multi-line question
// reads back the way it was typed instead of collapsing into one wrapped blob.
func echoSubmitted(prompt, cont, text string) {
	for i, seg := range strings.Split(text, "\n") {
		if i == 0 {
			fmt.Print(prompt + seg + "\r\n")
		} else {
			fmt.Print(cont + seg + "\r\n")
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

// termColumns is the same query without a session: the status line runs from
// plain `panda ask` too, where no raw-mode editor exists. Re-read on every
// paint so a window resize mid-run is honoured. 0 means "unknown".
func termColumns() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
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
//
// It leaves the terminal in peek mode, which is what both callers want: readLine
// polls so background notifications can interleave, and watchInterrupt polls so
// it never parks in read(2) past ctx completion. (It used to restore BLOCKING
// mode, which silently froze the notification drain until the next keystroke.)
func (t *termSession) readEscapeSequence() (string, bool) {
	if err := t.setRaw(true); err != nil {
		return "", false
	}
	defer func() { _ = t.setRaw(true) }()
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
	if strings.ContainsRune(line, '\n') {
		return buf, false // multi-line draft: not a command
	}
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

// tabCompleteAt completes whatever the cursor is on: the command name while
// the line is still one word, the argument once a space has been typed. resolve
// supplies the argument candidates (nil = command completion only); it is the
// REPL's argCandidates, which knows that /task takes a task id and /lang a
// locale code.
func tabCompleteAt(buf []rune, candidates []string, resolve argResolver) ([]rune, bool) {
	line := string(buf)
	token, cands := argCandidatesFor(line, resolve)
	if len(cands) == 0 {
		return tabComplete(buf, candidates)
	}
	head := line[:len(line)-len(token)]
	if len(cands) == 1 {
		if cands[0] == token {
			return buf, false
		}
		return []rune(head + cands[0] + " "), true
	}
	if p := commonPrefix(cands); len(p) > len(token) {
		return []rune(head + p), true
	}
	return buf, false
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
