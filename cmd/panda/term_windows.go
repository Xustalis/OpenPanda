//go:build windows

package main

// Windows terminal layer for the classic REPL. The unix implementation
// (term_unix.go) is a full raw-mode editor built on termios; Windows has no
// termios, so the classic REPL used to degrade to a bufio.Scanner line read —
// no cursor movement, no history, no Tab completion, and no way to cancel a
// running ask (Ctrl-C hit the console control handler in shutdownContext and
// killed the whole REPL instead).
//
// This file closes that gap with the Windows console API: ReadConsoleInputW
// delivers per-key KEY_EVENT records while ENABLE_LINE_INPUT /
// ENABLE_ECHO_INPUT / ENABLE_PROCESSED_INPUT are cleared, which gives us the
// same raw-by-raw control the unix editor has. With those flags cleared the
// console control handler chain is NOT invoked for Ctrl-C — it arrives as a
// key event (0x03) instead — so a running ask can be cancelled without
// killing the process, exactly like Esc on unix.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/charmbracelet/x/ansi"
	"github.com/erikgeiser/coninput"
	"golang.org/x/sys/windows"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// maxHistory mirrors the unix editor's cap (term_unix.go); the const lives
// per-platform because only these terminal files use it.
const maxHistory = 1000

type termSession struct {
	in     windows.Handle
	inMode uint32 // saved console input mode (cooked)
	raw    bool   // raw mode currently active

	keyCh         chan coninput.KeyEventRecord // one persistent reader goroutine
	readerStarted bool

	history     []string // oldest first, capped
	historyPath string   // "" = no persistence
	notifyCh    chan string

	loc     i18n.Locale
	argHint argResolver

	vt bool // VT sequences usable for redraw (see cliui.WindowsVTReady)

	// completion cycle state: the token being completed and the matches
	// cycled through on repeated Tab presses.
	compToken   string
	compMatches []string
	compIdx     int

	// pendingHigh holds a high surrogate waiting for its low half; console
	// input delivers supplementary characters as two consecutive key events.
	pendingHigh rune
}

// newTermSession returns a session when stdin is a real Windows console.
func newTermSession() *termSession {
	if !stdinIsTTY() {
		return nil
	}
	in, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return nil
	}
	var inMode uint32
	if err := windows.GetConsoleMode(in, &inMode); err != nil {
		return nil
	}
	t := &termSession{
		in:       in,
		inMode:   inMode,
		keyCh:    make(chan coninput.KeyEventRecord, 16),
		notifyCh: make(chan string, 16),
		// The REPL sets these after construction (repl.go), as on unix.
		vt: cliui.WindowsVTReady(),
	}
	return t
}

// readKeys is the session's single input pump: KEY_EVENT records flow into
// keyCh for whichever consumer owns the terminal right now (readLine or
// watchInterrupt — never both, the REPL main loop is sequential). Key-up
// events and non-key records are dropped here so consumers never see them.
//
// The pump starts lazily on the first setRaw, not at construction: the Bubble
// Tea TUI (the default front end on an interactive terminal) reads the same
// console handle through its own input layer, and a second concurrent
// ReadConsoleInputW would split the event stream between the two readers.
// Only the classic REPL ever enters raw mode, so only it starts the pump.
func (t *termSession) readKeys() {
	buf := make([]coninput.InputRecord, 16)
	for {
		n, err := coninput.ReadConsoleInput(t.in, buf)
		if err != nil {
			close(t.keyCh)
			return
		}
		for i := 0; i < int(n); i++ {
			rec, ok := buf[i].Unwrap().(coninput.KeyEventRecord)
			if !ok || !rec.KeyDown {
				continue
			}
			t.keyCh <- rec
		}
	}
}

// setRaw switches the console to raw input (no line buffering, no echo, no
// Ctrl-C special-casing) so key events arrive one keystroke at a time.
func (t *termSession) setRaw() error {
	if t.raw {
		return nil
	}
	mode := t.inMode &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(t.in, mode); err != nil {
		return err
	}
	t.raw = true
	if !t.readerStarted {
		t.readerStarted = true
		go t.readKeys()
	}
	return nil
}

// restore returns the console to its saved cooked mode.
func (t *termSession) restore() {
	if !t.raw {
		return
	}
	_ = windows.SetConsoleMode(t.in, t.inMode)
	t.raw = false
}

// initHistory loads previously entered lines from path and marks it for
// persistence, mirroring the unix implementation.
func (t *termSession) initHistory(path string) {
	t.historyPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	t.history = loadHistoryFile(data, maxHistory)
}

// recordHistory appends line (deduping consecutive repeats), keeps the cap,
// and rewrites the file (mirrors term_unix.go).
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

// deliver queues one background notification for the line editor to print
// between keystrokes; a full queue drops the line rather than blocking.
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

// ---- editing primitives (pure functions so tests can pin them) ----------

// insertRunes splices rs into buf at pos and returns the new buffer and the
// cursor position after the insertion.
func insertRunes(buf []rune, pos int, rs []rune) ([]rune, int) {
	if len(rs) == 0 {
		return buf, pos
	}
	out := make([]rune, 0, len(buf)+len(rs))
	out = append(out, buf[:pos]...)
	out = append(out, rs...)
	out = append(out, buf[pos:]...)
	return out, pos + len(rs)
}

// deleteLeftRunes removes n runes before pos.
func deleteLeftRunes(buf []rune, pos, n int) ([]rune, int) {
	if pos == 0 || n <= 0 {
		return buf, pos
	}
	if n > pos {
		n = pos
	}
	return append(buf[:pos-n:pos-n], buf[pos:]...), pos - n
}

// deleteRightRunes removes n runes at/after pos.
func deleteRightRunes(buf []rune, pos, n int) ([]rune, int) {
	if pos >= len(buf) || n <= 0 {
		return buf, pos
	}
	if pos+n > len(buf) {
		n = len(buf) - pos
	}
	return append(buf[:pos:pos], buf[pos+n:]...), pos
}

// deleteWordLeftRunes removes the word to the left of pos (spaces then
// non-spaces), Ctrl-W style.
func deleteWordLeftRunes(buf []rune, pos int) ([]rune, int) {
	end := pos
	for pos > 0 && buf[pos-1] == ' ' {
		pos--
	}
	for pos > 0 && buf[pos-1] != ' ' {
		pos--
	}
	return deleteLeftRunes(buf, end, end-pos)
}

// commonPrefix returns the longest prefix all candidates share. Truncation is
// rune-based — a byte cut would corrupt CJK candidates ("中文一"/"中文二"
// would share "中文\xe4" instead of "中文").
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := []rune(ss[0])
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, string(p)) {
			if len(p) <= 1 {
				return ""
			}
			p = p[:len(p)-1]
		}
	}
	return string(p)
}

// combineSurrogate pairs a high and low UTF-16 surrogate into one rune; ok is
// false when the halves do not form a valid pair.
func combineSurrogate(high, low rune) (rune, bool) {
	if high < 0xD800 || high > 0xDBFF || low < 0xDC00 || low > 0xDFFF {
		return 0, false
	}
	return utf16.DecodeRune(high, low), true
}

// evtRunes turns a key event's character into runes, pairing a high surrogate
// with the next event's low half (console input splits supplementary
// characters across two records).
func (t *termSession) evtRunes(ke coninput.KeyEventRecord) []rune {
	if ke.Char >= 0xD800 && ke.Char <= 0xDBFF {
		t.pendingHigh = ke.Char
		return nil
	}
	if t.pendingHigh != 0 {
		high := t.pendingHigh
		t.pendingHigh = 0
		if r, ok := combineSurrogate(high, ke.Char); ok {
			return []rune{r}
		}
		// A dangling high surrogate followed by a non-low unit: emit the
		// replacement rune for it, then fall through to the current char.
		rs := []rune{utf8Replacement}
		if ke.Char >= 0x20 {
			rs = append(rs, ke.Char)
		}
		return rs
	}
	// Control characters must never become buffer content. The combinations
	// the editor does understand (Ctrl-A/B/D/E/F/K/U/W, Enter, Tab, Esc,
	// Backspace) are consumed by readLine's switch before evtRunes runs; any
	// other control byte reaching this point (Ctrl-R, Ctrl-Z, …) is dropped
	// rather than spliced in as a raw control rune.
	if ke.Char < 0x20 || ke.Char == 0x7f {
		return nil
	}
	if ke.Char == 0 {
		return nil
	}
	return []rune{ke.Char}
}

const utf8Replacement = '\uFFFD'

// ---- completion ---------------------------------------------------------

// completeFor resolves the token under the cursor and the candidates matching
// it: the slash-command list while the line is still on the command name, the
// REPL's argument resolver after the first space.
func (t *termSession) completeFor(line string, completions []string) (string, []string) {
	if strings.HasPrefix(line, "/") && !strings.ContainsAny(line, " \t") {
		token := line
		var matches []string
		for _, c := range completions {
			if strings.HasPrefix(strings.ToLower(c), strings.ToLower(token)) {
				matches = append(matches, c)
			}
		}
		sort.Strings(matches)
		return token, matches
	}
	if t.argHint != nil {
		return argCandidatesFor(line, t.argHint)
	}
	return "", nil
}

// applyCompletion rewrites the line: an exact command name seals itself with
// a space (even when longer names share its prefix, /task vs /tasks), one
// match completes outright, several matches extend to their longest common
// prefix first and then cycle one candidate per Tab.
func (t *termSession) applyCompletion(buf []rune, completions []string) []rune {
	line := string(buf)
	token, matches := t.completeFor(line, completions)
	if len(matches) == 0 {
		return buf
	}
	commandPos := strings.HasPrefix(line, "/") && !strings.ContainsAny(line, " \t")
	if commandPos {
		for _, m := range matches {
			if m == token {
				return []rune(line + " ")
			}
		}
	}
	if len(matches) == 1 {
		repl := matches[0]
		if commandPos {
			repl += " "
		}
		return []rune(line[:len(line)-len(token)] + repl)
	}
	if t.compToken != token || len(t.compMatches) != len(matches) {
		t.compToken = token
		t.compMatches = matches
		t.compIdx = -1 // first Tab on a token extends to the common prefix
	}
	if t.compIdx == -1 {
		if lcp := commonPrefix(matches); len(lcp) > len(token) {
			return []rune(line[:len(line)-len(token)] + lcp)
		}
		t.compIdx = 0
	} else {
		t.compIdx = (t.compIdx + 1) % len(matches)
	}
	return []rune(line[:len(line)-len(token)] + matches[t.compIdx])
}

// ---- rendering ----------------------------------------------------------

// redraw repaints the editor line: return to the prompt, print prompt+buffer,
// clear to end of line, then step the cursor back to its logical position
// (CSI CUB wraps across lines, which keeps long wrapped buffers correct).
func (t *termSession) redraw(prompt string, promptW int, buf []rune, pos int) {
	if !t.vt {
		return
	}
	text := string(buf)
	full := promptW + cliui.DisplayWidth(string(buf[:pos]))
	width := promptW + cliui.DisplayWidth(text)
	fmt.Fprintf(os.Stdout, "\r%s%s\x1b[K", prompt, text)
	if width > full {
		fmt.Fprintf(os.Stdout, "\x1b[%dD", width-full)
	}
}

// printNotify interleaves a background notification: clear the editor row,
// print the note, redraw the editor on the fresh line.
func (t *termSession) printNotify(line, prompt string, promptW int, buf []rune, pos int) {
	if t.vt {
		fmt.Fprintf(os.Stdout, "\r\x1b[K%s\r\n", line)
		t.redraw(prompt, promptW, buf, pos)
		return
	}
	fmt.Println(line)
	t.redraw(prompt, promptW, buf, pos)
}

// echoAppend is the degraded non-VT rendering: the char is printed as typed.
func echoAppend(s string) {
	fmt.Print(s)
}

// readLine is the raw-mode line editor: UTF-16-aware insertion anywhere in
// the line (Left/Right/Home/End/Ctrl-A/E/B/F), backspace and forward delete
// (Backspace, Del), word/line kills (Ctrl-W/Ctrl-K/Ctrl-U), history recall
// (Up/Down, persisted across runs), Tab completion over command names and
// argument positions, and Enter to submit. Ctrl-C / Esc clear the line —
// with ENABLE_PROCESSED_INPUT off they arrive as key events, so a Ctrl-C at
// the prompt never kills the process.
func (t *termSession) readLine(prompt string, completions []string) (string, error) {
	if t == nil {
		return t.readLinePlain(prompt)
	}
	if err := t.setRaw(); err != nil {
		return t.readLinePlain(prompt)
	}
	defer t.restore()

	plain := ansi.Strip(prompt)
	promptW := cliui.DisplayWidth(plain)
	if !t.vt {
		fmt.Print(prompt)
	}

	var buf []rune
	pos := 0
	histIdx := -1
	var histDraft []rune
	t.compToken = ""
	t.compMatches = nil

	t.redraw(plain, promptW, buf, pos)
	for {
		select {
		case note, ok := <-t.notifyCh:
			if !ok {
				continue
			}
			t.printNotify(note, plain, promptW, buf, pos)
		case ke, ok := <-t.keyCh:
			if !ok {
				fmt.Println()
				return "", io.EOF
			}
			switch {
			case ke.Char == '\r':
				line := string(buf)
				fmt.Println()
				t.recordHistory(line)
				return line, nil
			case ke.Char == 0x04 && len(buf) == 0: // Ctrl-D on an empty line
				fmt.Println()
				return "", io.EOF
			case ke.Char == 0x03 || ke.Char == 0x1b: // Ctrl-C / Esc: clear line
				buf, pos = nil, 0
				t.compToken = ""
				t.compMatches = nil
				if !t.vt {
					fmt.Println()
				}
			case ke.Char == 0x01: // Ctrl-A: start of line
				pos = 0
			case ke.Char == 0x05: // Ctrl-E: end of line
				pos = len(buf)
			case ke.Char == 0x02: // Ctrl-B: back one char
				if pos > 0 {
					pos--
				}
			case ke.Char == 0x06: // Ctrl-F: forward one char
				if pos < len(buf) {
					pos++
				}
			case ke.Char == 0x15: // Ctrl-U: kill line
				buf, pos = nil, 0
				t.compToken = ""
				t.compMatches = nil
			case ke.Char == 0x0b: // Ctrl-K: kill to end
				buf = buf[:pos]
			case ke.Char == 0x17: // Ctrl-W: kill word left
				buf, pos = deleteWordLeftRunes(buf, pos)
			case ke.Char == '\t':
				buf = t.applyCompletion(buf, completions)
				pos = len(buf)
			case ke.Char == 0x08 || ke.Char == 0x7f || ke.VirtualKeyCode == coninput.VK_BACK:
				if pos > 0 {
					buf, pos = deleteLeftRunes(buf, pos, 1)
				}
			case ke.VirtualKeyCode == coninput.VK_DELETE:
				buf, pos = deleteRightRunes(buf, pos, 1)
			case ke.VirtualKeyCode == coninput.VK_LEFT:
				if pos > 0 {
					pos--
				}
			case ke.VirtualKeyCode == coninput.VK_RIGHT:
				if pos < len(buf) {
					pos++
				}
			case ke.VirtualKeyCode == coninput.VK_HOME:
				pos = 0
			case ke.VirtualKeyCode == coninput.VK_END:
				pos = len(buf)
			case ke.VirtualKeyCode == coninput.VK_UP:
				if len(t.history) == 0 {
					continue
				}
				if histIdx == -1 {
					histDraft = append([]rune(nil), buf...)
					histIdx = len(t.history) - 1
				} else if histIdx > 0 {
					histIdx--
				}
				buf = []rune(t.history[histIdx])
				pos = len(buf)
				t.compToken = ""
				t.compMatches = nil
			case ke.VirtualKeyCode == coninput.VK_DOWN:
				if histIdx == -1 {
					continue
				}
				histIdx++
				if histIdx >= len(t.history) {
					buf, histIdx = histDraft, -1
				} else {
					buf = []rune(t.history[histIdx])
				}
				pos = len(buf)
				t.compToken = ""
				t.compMatches = nil
			default:
				if rs := t.evtRunes(ke); len(rs) > 0 {
					buf, pos = insertRunes(buf, pos, rs)
					t.compToken = ""
					t.compMatches = nil
					if !t.vt {
						echoAppend(string(rs))
					}
				}
			}
			t.redraw(plain, promptW, buf, pos)
		}
	}
}

// readLinePlain is the last-resort fallback (no console at all): a plain
// scanner line read, exactly the old Windows behavior.
func (t *termSession) readLinePlain(prompt string) (string, error) {
	fmt.Print(prompt)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if sc.Scan() {
		return sc.Text(), nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

// drainKeyCh empties ch without blocking. A raw-mode consumer calls it on the
// way out: the input pump may have buffered stray keystrokes (channel
// capacity 16) that FlushConsoleInputBuffer cannot reach, and they must not
// replay at the next prompt. Pure so tests can pin it.
func drainKeyCh(ch chan coninput.KeyEventRecord) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// watchInterrupt monitors Esc / Ctrl-C while a long-running ask executes:
// the first press cancels the running context, a second within one second
// exits the process (double-tap, same contract as the unix editor). The
// console sits in raw mode so Ctrl-C arrives as a key event instead of going
// through the console control handler chain and killing the REPL. Runs until
// ctx completes, then restores cooked mode and flushes the ask-period input
// tail so stray keystrokes do not replay at the next prompt.
func (t *termSession) watchInterrupt(ctx context.Context, cancel context.CancelFunc, hint string) {
	if t == nil {
		return
	}
	if err := t.setRaw(); err != nil {
		return
	}
	defer func() {
		_ = windows.FlushConsoleInputBuffer(t.in)
		// The flush above clears the OS-level input queue; the pump may
		// already have pulled keys into keyCh, so drain that too — keys typed
		// during the ask must not replay at the next prompt.
		drainKeyCh(t.keyCh)
		t.restore()
	}()
	var first time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ke, ok := <-t.keyCh:
			if !ok {
				return
			}
			// Arrow keys and friends arrive as VK_* events with Char == 0, so
			// a lone Esc (0x1B) is unambiguous — no sequence peeking needed
			// the way the unix byte stream requires.
			if ke.Char != 0x1b && ke.Char != 0x03 {
				continue
			}
			if !first.IsZero() && time.Since(first) < time.Second {
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

// termColumns reports the terminal width from the console screen buffer, so
// the status line and tables truncate to the real window width; COLUMNS stays
// as an explicit override and 0 means "unknown, do not truncate".
func termColumns() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 20 {
		return n
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info); err == nil {
		if w := int(info.Window.Right-info.Window.Left) + 1; w >= 20 {
			return w
		}
	}
	return 0
}
