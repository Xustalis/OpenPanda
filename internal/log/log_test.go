package log

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"  info":  slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
		"garbage": slog.LevelInfo, // unknown → info
		"":        slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSetupEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	Setup("debug", &buf)
	slog.Info("hello", "node", "opi3b")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if rec["level"] != "INFO" || rec["msg"] != "hello" {
		t.Fatalf("bad record: %+v", rec)
	}
	if rec["node"] != "opi3b" {
		t.Fatalf("missing attr: %+v", rec)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	Setup("warn", &buf)
	slog.Info("should be dropped")
	slog.Warn("should appear")

	if bytes.Contains(buf.Bytes(), []byte("should be dropped")) {
		t.Fatalf("info log leaked past warn filter: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("should appear")) {
		t.Fatalf("warn log missing: %s", buf.String())
	}
}

func TestFromFallsBackToDefault(t *testing.T) {
	Setup("info", nil)
	l := From(context.Background())
	if l == nil {
		t.Fatalf("From returned nil")
	}
}
