package executil

import (
	"strings"
	"testing"
)

func TestCaptureKeepsShortOutput(t *testing.T) {
	var c Capture
	c.Write([]byte("hello"))
	if c.String() != "hello" {
		t.Fatalf("capture = %q, want %q", c.String(), "hello")
	}
}

func TestCaptureTruncatesLongOutput(t *testing.T) {
	var c Capture
	blob := strings.Repeat("x", captureCap+1024)
	n, err := c.Write([]byte(blob))
	if err != nil || n != len(blob) {
		t.Fatalf("write = (%d, %v), want (%d, nil)", n, err, len(blob))
	}
	got := c.String()
	if len(got) >= len(blob) {
		t.Fatalf("capture not bounded: %d >= %d", len(got), len(blob))
	}
	if !strings.Contains(got, "output truncated") {
		t.Fatalf("truncation marker missing")
	}
	// Further writes after truncation are discarded.
	c.Write([]byte("more"))
	if len(c.String()) != len(got) {
		t.Fatalf("post-truncation write grew the buffer")
	}
}
