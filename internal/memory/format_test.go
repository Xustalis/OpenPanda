package memory

import (
	"errors"
	"testing"
)

func TestParseMemAndBytesRoundTrip(t *testing.T) {
	in := "first entry\n§\nsecond entry\n§\nthird"
	m := ParseMem([]byte(in))
	if len(m.Entries) != 3 {
		t.Fatalf("got %d entries, want 3: %v", len(m.Entries), m.Entries)
	}
	if m.Entries[0] != "first entry" || m.Entries[1] != "second entry" || m.Entries[2] != "third" {
		t.Errorf("entries = %v", m.Entries)
	}

	// Round-trip preserves order and content.
	got := ParseMem(m.Bytes())
	if len(got.Entries) != 3 || got.Entries[1] != "second entry" {
		t.Errorf("round-trip mismatch: %v", got.Entries)
	}
}

func TestParseMemEmpty(t *testing.T) {
	if m := ParseMem(nil); len(m.Entries) != 0 {
		t.Errorf("nil should parse to empty, got %v", m.Entries)
	}
	if m := ParseMem([]byte("   \n  ")); len(m.Entries) != 0 {
		t.Errorf("whitespace should parse to empty, got %v", m.Entries)
	}
}

func TestAdd(t *testing.T) {
	var m MemFile
	if err := m.Add("alpha"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Add("beta"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Add(""); err == nil {
		t.Errorf("empty entry should error")
	}
	if err := m.Add("alpha"); err == nil {
		t.Errorf("duplicate entry should error")
	}
	if len(m.Entries) != 2 {
		t.Errorf("entries = %v", m.Entries)
	}
}

func TestAddEnforcesLimit(t *testing.T) {
	m := MemFile{Limit: 10}
	if err := m.Add("abcdef"); err != nil { // 6 chars, fits
		t.Fatalf("add within limit: %v", err)
	}
	if err := m.Add("abcdefghij"); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("add over limit: got %v, want ErrOverLimit", err)
	}
	if len(m.Entries) != 1 {
		t.Errorf("over-limit add should roll back, got %d entries", len(m.Entries))
	}
}

func TestReplaceAndRemove(t *testing.T) {
	m := MemFile{Entries: []string{"use Go for the core", "prefer dark mode"}}
	if err := m.Replace("dark mode", "prefer light mode"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if m.Entries[1] != "prefer light mode" {
		t.Errorf("replace failed: %v", m.Entries)
	}
	if err := m.Remove("light mode"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(m.Entries) != 1 || m.Entries[0] != "use Go for the core" {
		t.Errorf("remove failed: %v", m.Entries)
	}
}

func TestReplaceRemoveAmbiguous(t *testing.T) {
	// Both entries contain "dark mode", so substring matching must refuse.
	m := MemFile{Entries: []string{"prefer dark mode", "use dark mode in UI"}}
	if err := m.Replace("dark mode", "x"); err == nil {
		t.Errorf("ambiguous replace should error")
	}
	if err := m.Remove("dark mode"); err == nil {
		t.Errorf("ambiguous remove should error")
	}
	if len(m.Entries) != 2 {
		t.Errorf("ambiguous ops should not mutate, got %v", m.Entries)
	}
}

func TestReplaceRollsBackOnOverLimit(t *testing.T) {
	m := MemFile{Entries: []string{"short"}, Limit: 12}
	if err := m.Replace("short", "this replacement is far too long"); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("over-limit replace: got %v, want ErrOverLimit", err)
	}
	if m.Entries[0] != "short" {
		t.Errorf("over-limit replace should roll back, got %q", m.Entries[0])
	}
}

func TestChars(t *testing.T) {
	var m MemFile
	if m.Chars() != 0 {
		t.Errorf("empty chars = %d, want 0", m.Chars())
	}
	// Two entries serialize to "abc\n§\ndef" = 9 runes.
	m.Entries = []string{"abc", "def"}
	if m.Chars() != 9 {
		t.Errorf("chars = %d, want 9", m.Chars())
	}
}
