//go:build !linux && !darwin

package main

// Non-unix fallback (Windows): the REPL degrades to plain line reading —
// no raw-mode editing, Tab completion, or Esc/Ctrl-C interception.

import (
	"bufio"
	"context"
	"io"
	"os"
)

type termSession struct{}

func newTermSession() *termSession { return nil }

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
