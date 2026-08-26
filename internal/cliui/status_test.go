package cliui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a writer safe to read while the animation goroutine writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestStatusStaticMode is the piped-output contract: exactly one line per run,
// no escape sequences, no animation. `panda ask > file` must stay readable.
func TestStatusStaticMode(t *testing.T) {
	var buf bytes.Buffer
	st := NewStatus(&buf, Plain(), false)
	st.Start("thinking")
	st.Verb("routing")
	st.Note("running tool memory_get")
	st.SetTokens(1234)
	st.Stop()
	got := buf.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("static mode wrote %d lines, want 1: %q", strings.Count(got, "\n"), got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("static mode emitted an escape sequence: %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Errorf("static mode dropped the verb: %q", got)
	}
}

// TestStatusLiveNeverEmitsNewline is what keeps the status line to one physical
// row: it repaints with \r and only ever erases to end-of-line, so the caller's
// own output (and the REPL prompt after it) is never pushed around.
func TestStatusLiveNeverEmitsNewline(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, New(true, true), true).SetInterval(time.Millisecond)
	st.Start("thinking")
	waitFor(t, func() bool { return strings.Count(buf.String(), "\r") > 3 })
	st.Note("running tool memory_get")
	st.SetTokens(2048)
	d := st.Stop()

	got := buf.String()
	if strings.ContainsRune(got, '\n') {
		t.Errorf("live status emitted a newline: %q", got)
	}
	if !strings.HasSuffix(got, eraseLine+showCursor) {
		t.Errorf("live status did not erase its line and restore the cursor: %q", got)
	}
	if !strings.Contains(got, hideCursor) {
		t.Errorf("live status never hid the cursor: %q", got)
	}
	if !strings.Contains(got, "running tool memory_get") || !strings.Contains(got, "2k tokens") {
		t.Errorf("live status dropped a field: %q", got)
	}
	if d <= 0 {
		t.Errorf("Stop returned a non-positive duration: %v", d)
	}
	if st.Elapsed() != d {
		t.Errorf("Elapsed after Stop = %v, want the frozen %v", st.Elapsed(), d)
	}
}

// TestStatusSuspendClearsFirst covers the streaming case: answer text must land
// on a clean row, with the status line repainted below it afterwards.
func TestStatusSuspendClearsFirst(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, Plain(), true).SetInterval(time.Hour) // no animation noise
	st.Start("thinking")
	st.Log("answer line one")
	st.Stop()

	got := buf.String()
	i := strings.Index(got, "answer line one")
	if i < 0 {
		t.Fatalf("the logged line never printed: %q", got)
	}
	if !strings.Contains(got[:i], eraseLine) {
		t.Errorf("Log printed without erasing the status line first: %q", got)
	}
	if !strings.Contains(got[i:], "thinking") {
		t.Errorf("Log did not repaint the status line afterwards: %q", got)
	}
}

// TestStatusTruncatesToWidth guards the geometry: a status line wider than the
// terminal would wrap, and the next \r repaint would then corrupt the row above.
func TestStatusTruncatesToWidth(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, Plain(), true).SetInterval(time.Hour).SetWidth(func() int { return 20 })
	st.Start("thinking about a very long verb indeed")
	st.Stop()
	for _, seg := range strings.Split(buf.String(), "\r") {
		seg = strings.TrimSuffix(seg, "\x1b[K")
		seg = strings.TrimPrefix(seg, hideCursor)
		if w := DisplayWidth(seg); w > 19 {
			t.Errorf("painted %d columns into a 20-column terminal: %q", w, seg)
		}
	}
}

// TestStatusPreviewKeepsTheTailAndFitsTheRow: the preview exists so a long
// paragraph does not look like a stall, which only works if the newest words
// are the visible ones — and it must never widen the row past the terminal.
func TestStatusPreviewKeepsTheTailAndFitsTheRow(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, Plain(), true).SetInterval(time.Hour).SetWidth(func() int { return 60 })
	st.Start("thinking")
	st.Preview("the quick brown fox jumps over the lazy dog and keeps going")
	st.Stop()
	got := buf.String()
	if !strings.Contains(got, "keeps going") {
		t.Errorf("preview dropped the newest words: %q", got)
	}
	if strings.Contains(got, "the quick brown fox") {
		t.Errorf("preview kept the oldest words instead of the newest: %q", got)
	}
	for _, seg := range strings.Split(got, "\r") {
		seg = strings.TrimSuffix(seg, "\x1b[K")
		seg = strings.TrimPrefix(seg, hideCursor)
		if w := DisplayWidth(seg); w > 59 {
			t.Errorf("preview painted %d columns into a 60-column terminal: %q", w, seg)
		}
	}
}

// TestStatusPreviewNeedsAKnownWidth: without a width the row length is
// unbounded, and a wrapped status line breaks the \r repaint — so the preview
// is simply not painted.
func TestStatusPreviewNeedsAKnownWidth(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, Plain(), true).SetInterval(time.Hour)
	st.Start("thinking")
	st.Preview("some streamed words")
	st.Stop()
	if strings.Contains(buf.String(), "streamed words") {
		t.Errorf("preview painted without a known terminal width: %q", buf.String())
	}
}

// TestStatusIdempotentStopAndNoopSetters lets callers `defer st.Stop()` and call
// setters whenever, without tracking whether a run is in flight.
func TestStatusIdempotentStopAndNoopSetters(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, Plain(), true).SetInterval(time.Millisecond)
	st.Note("before any run") // must not paint
	st.Hint("esc to interrupt")
	if st.Running() {
		t.Error("Running() true before Start")
	}
	if got := buf.String(); got != "" {
		t.Errorf("a setter painted outside a run: %q", got)
	}
	st.Start("thinking")
	first := st.Stop()
	if second := st.Stop(); second != first {
		t.Errorf("second Stop returned %v, want the frozen %v", second, first)
	}
	if st.Running() {
		t.Error("Running() true after Stop")
	}
}

// TestStatusConcurrentWriters is the race-detector target: the animation
// goroutine and a caller printing through Suspend share one writer.
func TestStatusConcurrentWriters(t *testing.T) {
	buf := &syncBuf{}
	st := NewStatus(buf, New(true, true), true).SetInterval(time.Millisecond)
	st.Start("thinking")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				st.Log("line")
				st.SetTokens(int64(n*100 + j))
				st.Verb("routing")
				st.Note("tool")
			}
		}(i)
	}
	wg.Wait()
	st.Stop()
}

// waitFor polls cond for up to a second — long enough for a 1ms ticker on a
// loaded CI box, short enough not to hang the suite.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the status line to animate")
}
