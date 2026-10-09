// SPDX-License-Identifier: AGPL-3.0-or-later

package log

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newConsoleLogger(w *bytes.Buffer) *slog.Logger {
	return slog.New(Console("debug", w))
}

func TestConsoleFormat(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // keep the assertion text clean of ANSI
	var buf bytes.Buffer
	newConsoleLogger(&buf).Info("node up", "addr", "127.0.0.1:9000", "peers", 3)
	line := buf.String()
	if !strings.HasPrefix(line, time.Now().Format("15:04")) {
		t.Fatalf("no HH:MM prefix: %q", line)
	}
	for _, want := range []string{"INFO", "node up", "addr=127.0.0.1:9000", "peers=3"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q", want, line)
		}
	}
}

func TestConsoleLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(Console("warn", &buf))
	l.Debug("hidden")
	l.Info("hidden too")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") {
		t.Fatalf("level filter leaked: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "shown") {
		t.Fatalf("warn missing: %q", buf.String())
	}
}

func TestConsoleErrorAttr(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	newConsoleLogger(&buf).Error("dispatch failed", "err", errors.New("no such file or directory"))
	line := buf.String()
	if !strings.Contains(line, "err=\"no such file or directory\"") && !strings.Contains(line, "err=no such file") {
		t.Fatalf("error not rendered as message: %q", line)
	}
	if strings.Contains(line, "0x") || strings.Contains(line, "{") {
		t.Fatalf("Go dump leaked: %q", line)
	}
}

func TestConsoleGroupFlatten(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	l := newConsoleLogger(&buf).WithGroup("queue")
	l.Info("dispatched", "task", "abc")
	if !strings.Contains(buf.String(), "queue.task=abc") {
		t.Fatalf("group not flattened: %q", buf.String())
	}
}

func TestConsoleNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	newConsoleLogger(&buf).Error("boom")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("NO_COLOR ignored: %q", buf.String())
	}
}

func TestConsoleColorWhenAllowed(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	var buf bytes.Buffer
	newConsoleLogger(&buf).Error("boom")
	if !strings.Contains(buf.String(), "\x1b[31m") {
		t.Fatalf("no colour: %q", buf.String())
	}
}

func TestConsoleQuotedValues(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	newConsoleLogger(&buf).Info("x", "msg", "has spaces")
	if !strings.Contains(buf.String(), `msg="has spaces"`) {
		t.Fatalf("space value not quoted: %q", buf.String())
	}
}
