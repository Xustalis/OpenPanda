// SPDX-License-Identifier: AGPL-3.0-or-later

// Package log provides structured JSON logging over slog.
//
// The core daemon logs JSON lines so they can be shipped to journald,
// Splunk, or a file without a custom formatter.
package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/security"
)

// Level strings accepted from config.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// ParseLevel maps a config level string to slog.Level. Unknown strings
// default to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup configures the default slog handler to write JSON to w.
func Setup(level string, w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(redactedLogWriter{w}, &slog.HandlerOptions{Level: ParseLevel(level)})
	slog.SetDefault(slog.New(h))
}

// JSONHandler writes one complete record per call.
type redactedLogWriter struct{ io.Writer }

func (w redactedLogWriter) Write(p []byte) (int, error) {
	clean := security.RedactJSON(p)
	if len(clean) == 0 || clean[len(clean)-1] != '\n' {
		clean = append(clean, '\n')
	}
	n, err := w.Writer.Write(clean)
	if err == nil && n != len(clean) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// SetupTTY is Setup for a process whose log sink IS the terminal the user is
// looking at: compact console lines instead of JSON. isTTY should reflect the
// actual sink (stdout for the daemon's default path); a false keeps JSON.
func SetupTTY(level string, w io.Writer, isTTY bool) {
	if !isTTY {
		Setup(level, w)
		return
	}
	if w == nil {
		w = os.Stdout
	}
	slog.SetDefault(slog.New(Console(level, w)))
}

// CtxKey is the key under which a logger may be stored in a context.
type CtxKey struct{}

// From returns the logger stored in ctx, or the default logger.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(CtxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
