// SPDX-License-Identifier: AGPL-3.0-or-later

package log

// A compact console handler for when the daemon runs in the foreground on a
// terminal: `panda daemon` is also the developer-facing "run it and watch"
// path, and raw JSON lines answer questions nobody asked while burying the
// ones they did. The format is `HH:MM:SS LEVEL msg k=v k=v`, coloured by
// level; attributes keep slog order. Piped output still gets the JSON
// handler — this is for eyes, not pipes.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// consoleHandler is a slog.Handler rendering records as aligned one-liners.
type consoleHandler struct {
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
	group string
	mu    *sync.Mutex
}

// Console returns a handler writing compact text lines to w. Callers pick it
// only when the sink is a terminal (fileIsTTY) — anything else keeps JSON.
func Console(level string, w io.Writer) slog.Handler {
	return &consoleHandler{w: w, level: ParseLevel(level), mu: &sync.Mutex{}}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(consoleLevel(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	attrs := h.attrs
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	for _, a := range attrs {
		b.WriteByte(' ')
		b.WriteString(consoleAttr(h.group, a))
	}
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &n
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	n := *h
	if h.group == "" {
		n.group = name
	} else {
		n.group = h.group + "." + name
	}
	return &n
}

// consoleLevel renders the level as a fixed-width word; warnings and errors
// get ANSI colour when the terminal palette wants it. Keeping the colour in
// this package (rather than reaching into cliui) leaves internal/log free of
// the TTY-detection machinery — the caller decided "console" already.
func consoleLevel(l slog.Level) string {
	if os.Getenv("NO_COLOR") != "" {
		switch {
		case l >= slog.LevelError:
			return "ERROR"
		case l >= slog.LevelWarn:
			return "WARN "
		case l >= slog.LevelInfo:
			return "INFO "
		default:
			return "DEBUG"
		}
	}
	switch {
	case l >= slog.LevelError:
		return "\x1b[31mERROR\x1b[0m"
	case l >= slog.LevelWarn:
		return "\x1b[33mWARN \x1b[0m"
	case l >= slog.LevelInfo:
		return "\x1b[36mINFO \x1b[0m"
	default:
		return "\x1b[90mDEBUG\x1b[0m"
	}
}

// consoleAttr renders one attribute. Group names fold into the key as
// "group.key" so nested WithGroup output stays one flat readable line; errors
// keep their message text rather than Go's %#v dump.
func consoleAttr(prefix string, a slog.Attr) string {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return ""
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	var val string
	switch a.Value.Kind() {
	case slog.KindString:
		val = a.Value.String()
	case slog.KindTime:
		val = a.Value.Time().Format(time.RFC3339)
	case slog.KindGroup:
		var parts []string
		for _, ga := range a.Value.Group() {
			parts = append(parts, consoleAttr(key, ga))
		}
		return strings.Join(parts, " ")
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			val = err.Error()
			break
		}
		val = fmt.Sprintf("%v", a.Value.Any())
	default:
		val = a.Value.String()
	}
	if strings.ContainsAny(val, " \t\"") {
		val = fmt.Sprintf("%q", val)
	}
	return key + "=" + val
}
