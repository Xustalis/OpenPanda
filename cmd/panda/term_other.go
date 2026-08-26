//go:build !linux && !darwin

package main

// Non-unix fallback (Windows): the REPL degrades to plain line reading —
// no raw-mode editing, Tab completion, or Esc/Ctrl-C interception.

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

type termSession struct {
	history     []string // oldest first, capped (mirrors the unix session)
	historyPath string   // "" = no persistence

	// loc mirrors the unix session's field so the shared REPL code compiles
	// unchanged. This editor has no in-place UI of its own to label — no
	// Ctrl-R search line — so nothing reads it here.
	loc i18n.Locale
	// argHint likewise: a scanner line read has no Tab key to complete on.
	argHint argResolver
}

func newTermSession() *termSession { return nil }

// initHistory loads previously entered lines from path and marks it for
// persistence, mirroring the unix implementation so shared REPL code compiles.
func (t *termSession) initHistory(path string) {
	t.historyPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	t.history = loadHistoryFile(data, 1000)
}

func (t *termSession) readLine(prompt string, completions []string) (string, error) {
	// Callers gate on stdinIsTTY; on non-unix TTYs we still fall back to a
	// scanner line read rather than failing.
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return sc.Text(), nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func (t *termSession) watchInterrupt(ctx context.Context, cancel context.CancelFunc, hint string) {
	<-ctx.Done()
}

// deliver has no in-place line editor to interleave with on this platform;
// false tells the caller to print the line itself.
func (t *termSession) deliver(line string) bool { return false }

func (t *termSession) restore() {}

// termColumns reports the terminal width for the status line. No ioctl here, so
// COLUMNS is the only hint available; 0 means "unknown, do not truncate".
func termColumns() int {
	n, err := strconv.Atoi(os.Getenv("COLUMNS"))
	if err != nil || n < 20 {
		return 0
	}
	return n
}
