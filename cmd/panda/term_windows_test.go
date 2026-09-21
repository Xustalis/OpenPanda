//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/erikgeiser/coninput"
)

// The editing primitives are pure functions so the Windows editor's logic can
// be pinned without a console: the console-API surface itself (SetConsoleMode,
// ReadConsoleInput) is exercised by the real binary in CI's windows runner.

func TestInsertRunes(t *testing.T) {
	buf := []rune("ab")
	got, pos := insertRunes(buf, 1, []rune("XY"))
	if string(got) != "aXYb" || pos != 3 {
		t.Fatalf("insertRunes(ab,1,XY) = %q pos %d, want aXYb pos 3", string(got), pos)
	}
	got, pos = insertRunes(buf, 2, []rune("Z"))
	if string(got) != "abZ" || pos != 3 {
		t.Fatalf("insert at end = %q pos %d, want abZ pos 3", string(got), pos)
	}
	if got, pos := insertRunes(nil, 0, []rune("x")); string(got) != "x" || pos != 1 {
		t.Fatalf("insert into empty = %q pos %d", string(got), pos)
	}
}

func TestDeleteLeftRight(t *testing.T) {
	if got, pos := deleteLeftRunes([]rune("abc"), 2, 1); string(got) != "ac" || pos != 1 {
		t.Fatalf("deleteLeft(abc,2,1) = %q pos %d, want ac pos 1", string(got), pos)
	}
	if got, pos := deleteLeftRunes([]rune("abc"), 0, 1); string(got) != "abc" || pos != 0 {
		t.Fatalf("deleteLeft at 0 changed the buffer: %q", string(got))
	}
	if got, pos := deleteRightRunes([]rune("abc"), 1, 1); string(got) != "ac" || pos != 1 {
		t.Fatalf("deleteRight(abc,1,1) = %q pos %d, want ac pos 1", string(got), pos)
	}
	if got, _ := deleteRightRunes([]rune("abc"), 2, 5); string(got) != "ab" {
		t.Fatalf("deleteRight overrun = %q, want ab", string(got))
	}
	if got, pos := deleteRightRunes([]rune("abc"), 3, 1); string(got) != "abc" || pos != 3 {
		t.Fatalf("deleteRight at end changed the buffer: %q", string(got))
	}
}

func TestDeleteWordLeft(t *testing.T) {
	cases := []struct {
		in   string
		pos  int
		want string
	}{
		{"hello world", 11, "hello "},
		{"a  b", 4, "a  "},   // spaces are part of the word's left margin
		{"a b", 1, " b"},     // word under cursor removed
		{"word", 4, ""},      // whole word
		{"", 0, ""},          // nothing to kill
		{"中文 abc", 6, "中文 "}, // CJK word boundary works off spaces
	}
	for _, c := range cases {
		got, _ := deleteWordLeftRunes([]rune(c.in), c.pos)
		if string(got) != c.want {
			t.Errorf("deleteWordLeft(%q, %d) = %q, want %q", c.in, c.pos, string(got), c.want)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"apple", "apply", "app"}, "app"},
		{[]string{"a", "b"}, ""},
		{[]string{"same"}, "same"},
		{nil, ""},
		{[]string{"中文一", "中文二"}, "中文"},
	}
	for _, c := range cases {
		if got := commonPrefix(c.in); got != c.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCombineSurrogate(t *testing.T) {
	// U+1F600 (😀) = D83D DE00
	if r, ok := combineSurrogate(0xD83D, 0xDE00); !ok || r != '😀' {
		t.Fatalf("combineSurrogate(D83D,DE00) = %U ok=%v, want U+1F600", r, ok)
	}
	if _, ok := combineSurrogate(0xDE00, 0xD83D); ok {
		t.Fatal("swapped surrogate halves reported a valid pair")
	}
	if _, ok := combineSurrogate('a', 'b'); ok {
		t.Fatal("BMP chars reported a valid pair")
	}
}

// TestApplyCompletionExercise drives the completion state machine without a
// console: the pure helpers behind it (completeFor is a method on the
// termSession, but the argResolver it calls is injectable).
func TestApplyCompletionCommandPosition(t *testing.T) {
	ts := &termSession{}
	completions := []string{"/task", "/tasks", "/lang"}

	// An exact command name seals itself with a space even though a longer
	// name shares its prefix.
	got := ts.applyCompletion([]rune("/task"), completions)
	if string(got) != "/task " {
		t.Fatalf("exact-match completion = %q, want \"/task \"", string(got))
	}
	// Several matches: first Tab extends to the common prefix.
	got = ts.applyCompletion([]rune("/ta"), completions)
	if string(got) != "/task" {
		t.Fatalf("lcp completion = %q, want /task", string(got))
	}
	// When the common prefix is already fully typed, Tabs cycle through the
	// candidates sharing it.
	ts.compToken, ts.compMatches, ts.compIdx = "", nil, 0
	shared := []string{"/apple-one", "/apple-two"}
	got = ts.applyCompletion([]rune("/apple"), shared)
	if string(got) != "/apple-" {
		t.Fatalf("lcp extension = %q, want /apple-", string(got))
	}
	got = ts.applyCompletion([]rune("/apple-"), shared)
	if string(got) != "/apple-one" {
		t.Fatalf("cycle 1 = %q, want /apple-one", string(got))
	}
	got = ts.applyCompletion([]rune("/apple-"), shared)
	if string(got) != "/apple-two" {
		t.Fatalf("cycle 2 = %q, want /apple-two", string(got))
	}
	// A prefix with exactly one candidate completes outright with a space.
	got = ts.applyCompletion([]rune("/lan"), completions)
	if string(got) != "/lang " {
		t.Fatalf("single candidate = %q, want \"/lang \"", string(got))
	}
}

func TestApplyCompletionArguments(t *testing.T) {
	ts := &termSession{argHint: func(cmd string, args []string) []string {
		if cmd == "task" && len(args) == 1 {
			return []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
		}
		return nil
	}}
	got := ts.applyCompletion([]rune("/task 1111"), nil)
	if string(got) != "/task 11111111-1111-4111-8111-111111111111" {
		t.Fatalf("argument completion = %q", string(got))
	}
	// No resolver and no matches: line untouched.
	ts2 := &termSession{}
	if got := ts2.applyCompletion([]rune("/ask why"), nil); string(got) != "/ask why" {
		t.Fatalf("completion without candidates changed the line: %q", string(got))
	}
}

func TestCompleteForFiltersCaseInsensitively(t *testing.T) {
	ts := &termSession{}
	token, matches := ts.completeFor("/TASK", []string{"/task", "/tasks", "/lang"})
	if token != "/TASK" {
		t.Fatalf("token = %q, want /TASK", token)
	}
	if strings.Join(matches, "|") != "/task|/tasks" {
		t.Fatalf("matches = %v, want [/task /tasks]", matches)
	}
}

// TestEvtRunesFiltersControlCharacters guards the buffer against raw control
// bytes: combinations the editor does not handle (Ctrl-R, Ctrl-Z, …) must be
// dropped instead of spliced into the line — the handled ones are consumed by
// readLine's switch before evtRunes ever sees them.
func TestEvtRunesFiltersControlCharacters(t *testing.T) {
	ts := &termSession{}
	for _, r := range []rune{0x01, 0x04, 0x12, 0x1a, 0x03, 0x1b, '\r', '\t', 0x7f} {
		if rs := ts.evtRunes(coninput.KeyEventRecord{Char: r}); rs != nil {
			t.Errorf("control char 0x%02X leaked into the buffer: %v", r, rs)
		}
	}
	if rs := ts.evtRunes(coninput.KeyEventRecord{Char: 'a'}); len(rs) != 1 || rs[0] != 'a' {
		t.Errorf("printable char changed: %v", rs)
	}
	// UTF-16 surrogate pairing still works after the filter.
	ts.pendingHigh = 0xD83D
	if rs := ts.evtRunes(coninput.KeyEventRecord{Char: 0xDE00}); len(rs) != 1 || rs[0] != '😀' {
		t.Errorf("surrogate pair after filter = %v, want U+1F600", rs)
	}
}

func TestDrainKeyCh(t *testing.T) {
	ch := make(chan coninput.KeyEventRecord, 4)
	ch <- coninput.KeyEventRecord{Char: 'x'}
	ch <- coninput.KeyEventRecord{Char: 'y'}
	drainKeyCh(ch)
	if len(ch) != 0 {
		t.Fatalf("drainKeyCh left %d records behind", len(ch))
	}
	drainKeyCh(ch) // an empty channel must not block
}
