package main

import "testing"

// TestHistoryCodecRoundTrip is what makes multi-line prompts recallable: the
// file is one entry per physical line, so a pasted block has to survive the
// round trip as ONE entry rather than come back as N.
func TestHistoryCodecRoundTrip(t *testing.T) {
	entries := []string{
		"plain question",
		"line one\nline two\nline three",
		`a windows path C:\temp and a literal \n`,
		"trailing backslash \\",
	}
	got := loadHistoryFile(encodeHistoryFile(entries), 0)
	if len(got) != len(entries) {
		t.Fatalf("round trip changed the entry count: %d → %d (%q)", len(entries), len(got), got)
	}
	for i, want := range entries {
		if got[i] != want {
			t.Errorf("entry %d round-tripped to %q, want %q", i, got[i], want)
		}
	}
}

// TestHistoryDecodeLegacy keeps history files written by older builds readable:
// they stored entries verbatim, so an unknown escape must pass through both
// bytes instead of being interpreted.
func TestHistoryDecodeLegacy(t *testing.T) {
	cases := map[string]string{
		`C:\temp`:     `C:\temp`, // \t is not an escape this codec defines
		`no escapes`:  `no escapes`,
		`ends with \`: `ends with \`,
		`a\nb`:        "a\nb",
		`a\\nb`:       `a\nb`,
	}
	for in, want := range cases {
		if got := decodeHistoryLine(in); got != want {
			t.Errorf("decodeHistoryLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadHistoryFileCaps drops blank lines and keeps the NEWEST entries when
// the file outgrows the cap.
func TestLoadHistoryFileCaps(t *testing.T) {
	got := loadHistoryFile([]byte("a\n\nb\nc\n"), 2)
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("loadHistoryFile capped to %q, want [b c]", got)
	}
}
