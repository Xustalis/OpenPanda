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
	fd  int
	old *unix.Termios
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
	return &termSession{fd: fd, old: old}
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

// readLine is the raw-mode line editor: UTF-8 aware insertion, backspace,
// Ctrl-U (clear), Tab completion over the given word list, Enter to submit,
// Ctrl-D on an empty line = EOF. A lone Ctrl-C clears the line and returns
// it empty with errCtrlC; a second Ctrl-C within one second exits the
// process (the double-tap exit contract, like codex).
func (t *termSession) readLine(prompt string, completions []string) (string, error) {
	if err := t.setRaw(false); err != nil {
		return "", err
	}
	defer t.restore()
	fmt.Print(prompt)

	var buf []rune
	lastCtrlC := time.Time{}
	flush := func() {
		fmt.Print("\r\x1b[K" + prompt + string(buf))
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
		switch b {
		case 0x0d, 0x0a: // Enter
			fmt.Print("\r\n")
			return string(buf), nil
		case 0x04: // Ctrl-D
			if len(buf) == 0 {
				fmt.Print("\r\n")
				return "", io.EOF
			}
		case 0x03: // Ctrl-C
			if !lastCtrlC.IsZero() && time.Since(lastCtrlC) < time.Second {
				fmt.Print("\r\n")
				os.Exit(130)
			}
			lastCtrlC = time.Now()
			buf = buf[:0]
			fmt.Print("^C\r\n" + prompt)
		case 0x15: // Ctrl-U: clear line
			buf = buf[:0]
			flush()
		case 0x7f, 0x08: // Backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				flush()
			}
		case 0x09: // Tab: complete the leading slash command
			var matched bool
			buf, matched = tabComplete(buf, completions)
			if matched {
				flush()
			}
		case 0x1b: // Esc or an escape sequence (arrows etc.)
			if seq, lone := t.readEscapeSequence(); lone {
				_ = seq
				// A lone Esc at the prompt is ignored (its interrupt role
				// applies while a task is running — see watchInterrupt).
			}
		default:
			if b < 0x20 {
				continue // other control chars: ignore
			}
			// Assemble one UTF-8 rune (multi-byte sequences arrive split).
			r, extra := decodeRune(b, t)
			buf = append(buf, r)
			fmt.Print(string(r))
			_ = extra
		}
	}
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
// completes in place; several print the option list and re-draw the prompt.
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
		fmt.Print("\r\n  " + strings.Join(matches, "   ") + "\r\n")
		return buf, true
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
				// Swallow CSI tails so a stray arrow key never cancels.
				if _, lone := t.readEscapeSequence(); !lone {
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
