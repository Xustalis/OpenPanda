package guard

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestLogger returns a logger writing to a buffer plus the buffer.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	// A mutex keeps concurrent test goroutines from interleaving writes
	// mid-line (bytes.Buffer is not safe for concurrent use).
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockingWriter{w: &buf, mu: &mu}, nil)
	return slog.New(h), &buf
}

type lockingWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockingWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockingWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

func waitFor(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestGoRecoversPanicAndLogsStack pins the core contract: a panic inside a
// guarded goroutine never escapes, and the log line carries both the goroutine
// name and a real stack trace.
func TestGoRecoversPanicAndLogsStack(t *testing.T) {
	logger, buf := newTestLogger()
	done := make(chan struct{})
	Go(logger, "test-loop", func() { close(done) }, func() {
		panic("boom")
	})
	waitFor(t, done, "onPanic callback")
	out := buf.String()
	if !strings.Contains(out, "goroutine panic recovered") {
		t.Errorf("log missing the recovered-panic marker: %q", out)
	}
	if !strings.Contains(out, "name=test-loop") {
		t.Errorf("log missing the goroutine name: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("log missing the panic value: %q", out)
	}
	if !strings.Contains(out, "TestGoRecoversPanicAndLogsStack") {
		t.Errorf("log missing the stack trace (expected the test frame): %q", out)
	}
}

// TestCallRecoversPanic covers the synchronous form: it must return normally
// after recovering, so a blocking caller (the bus handler) keeps running.
func TestCallRecoversPanic(t *testing.T) {
	logger, buf := newTestLogger()
	called := false
	Call(logger, "test-conn", func() { called = true }, func() {
		panic("conn-loop died")
	})
	if !called {
		t.Error("onPanic was not invoked")
	}
	if !strings.Contains(buf.String(), "name=test-conn") {
		t.Errorf("log missing the goroutine name: %q", buf.String())
	}
}

// TestOnPanicNilIsSafe: per-connection loops pass nil onPanic (log and let the
// connection die); that path must not panic itself.
func TestOnPanicNilIsSafe(t *testing.T) {
	logger, _ := newTestLogger()
	done := make(chan struct{})
	Go(logger, "conn", nil, func() {
		defer close(done)
		panic("single-conn failure")
	})
	waitFor(t, done, "deferred close after panic")
}

// TestNormalFunctionUnaffected: fn that returns cleanly runs exactly once,
// logs nothing, and never fires onPanic.
func TestNormalFunctionUnaffected(t *testing.T) {
	logger, buf := newTestLogger()
	done := make(chan struct{})
	onPanicFired := false
	ran := 0
	Go(logger, "healthy", func() { onPanicFired = true }, func() {
		ran++
		close(done)
	})
	waitFor(t, done, "fn to run")
	if ran != 1 {
		t.Errorf("fn ran %d times, want 1", ran)
	}
	if onPanicFired {
		t.Error("onPanic fired for a clean return")
	}
	if out := buf.String(); out != "" {
		t.Errorf("unexpected log output for a clean run: %q", out)
	}
	// The synchronous form shares the contract.
	Call(logger, "healthy", func() { onPanicFired = true }, func() { ran++ })
	if ran != 2 {
		t.Errorf("Call fn ran %d times total, want 2", ran)
	}
}

// TestNilLoggerFallsBackToDefault ensures a nil logger does not crash the
// recovery path itself (the one place a panic really must not escape).
func TestNilLoggerFallsBackToDefault(t *testing.T) {
	done := make(chan struct{})
	Go(nil, "no-logger", func() { close(done) }, func() {
		panic("fallback")
	})
	waitFor(t, done, "onPanic with nil logger")
}
