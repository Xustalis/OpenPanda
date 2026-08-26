//go:build linux || darwin

package main

import (
	"strings"
	"testing"
)

// TestSplitLogicalLines pins the two boundary shapes the renderer depends on:
// an empty buffer is ONE (empty) line, not zero, and a trailing newline leaves
// a real trailing line for the cursor to sit on.
func TestSplitLogicalLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{""}},
		{"one", []string{"one"}},
		{"a\nb", []string{"a", "b"}},
		{"a\n", []string{"a", ""}},
		{"\na", []string{"", "a"}},
		{"a\n\nb", []string{"a", "", "b"}},
	}
	for _, c := range cases {
		segs := splitLogicalLines([]rune(c.in))
		got := make([]string, 0, len(segs))
		for _, s := range segs {
			got = append(got, string(s))
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("splitLogicalLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLineBounds covers Home/End and the Up/Down line walk: every index inside
// a line must resolve to the same pair of bounds, and the bounds must exclude
// the newline itself (a cursor never sits "on" the newline).
func TestLineBounds(t *testing.T) {
	buf := []rune("ab\ncde\n\nf")
	//             012 3456 7 8
	cases := []struct{ pos, start, end int }{
		{0, 0, 2}, {1, 0, 2}, {2, 0, 2}, // "ab"
		{3, 3, 6}, {5, 3, 6}, {6, 3, 6}, // "cde"
		{7, 7, 7},            // the empty line
		{8, 8, 9}, {9, 8, 9}, // "f"
	}
	for _, c := range cases {
		if got := lineStartAt(buf, c.pos); got != c.start {
			t.Errorf("lineStartAt(pos=%d) = %d, want %d", c.pos, got, c.start)
		}
		if got := lineEndAt(buf, c.pos); got != c.end {
			t.Errorf("lineEndAt(pos=%d) = %d, want %d", c.pos, got, c.end)
		}
	}
	if got := lineStartAt([]rune("abc"), 99); got != 0 {
		t.Errorf("lineStartAt past the end = %d, want 0", got)
	}
}

// TestPastedRunesSanitizes is the security-relevant one: a pasted terminal dump
// carries escape sequences, and re-emitting those would let pasted text repaint
// the screen (or worse, on terminals with reporting sequences enabled).
func TestPastedRunesSanitizes(t *testing.T) {
	got := string(pastedRunes("a\x1b[31mred\x07b"))
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("paste kept control bytes: %q", got)
	}
	if !strings.Contains(got, "red") {
		t.Errorf("paste dropped the visible text: %q", got)
	}
}

// TestPastedRunesNewlines checks the whole point of bracketed paste: the line
// breaks inside a paste survive as buffer content (one prompt), while the
// trailing ones are dropped so the paste does not look like a submit.
func TestPastedRunesNewlines(t *testing.T) {
	got := string(pastedRunes("one\r\ntwo\rthree\n\n"))
	if got != "one\ntwo\nthree" {
		t.Errorf("pastedRunes = %q, want %q", got, "one\ntwo\nthree")
	}
	// A tab counts zero columns in runeWidth, so a literal one would desync the
	// wrap math; it must arrive widened.
	if got := string(pastedRunes("\tx")); got != pasteTab+"x" {
		t.Errorf("pasted tab = %q, want %q", got, pasteTab+"x")
	}
}

// TestContinuationPrefixWidth keeps the continuation marker exactly as wide as
// the prompt: anything narrower and every wrapped row of a multi-line draft
// would sit one column off.
func TestContinuationPrefixWidth(t *testing.T) {
	for _, w := range []int{1, 2, 3, 8} {
		if got := displayWidth(continuationPrefix(w)); got < w {
			t.Errorf("continuationPrefix(%d) is %d columns wide, want ≥ %d", w, got, w)
		}
	}
}

// TestTabCompleteSkipsMultiline stops Tab from rewriting a pasted block: a
// draft with a newline in it is prose or code, never a slash command, and
// completing it would silently replace the user's text.
func TestTabCompleteSkipsMultiline(t *testing.T) {
	in := []rune("/he\nlp")
	got, completed := tabComplete(in, []string{"/help"})
	if completed || string(got) != string(in) {
		t.Errorf("tabComplete rewrote a multi-line draft: %q (completed=%v)", string(got), completed)
	}
	if got, completed := tabComplete([]rune("/hel"), []string{"/help"}); !completed || string(got) != "/help " {
		t.Errorf("single-line completion broke: %q (completed=%v)", string(got), completed)
	}
}

// TestTabCompleteAtArguments pins the argument path: only the token under the
// cursor is replaced, the rest of the line survives, and a many-way slot
// completes as far as the common prefix and no further.
func TestTabCompleteAtArguments(t *testing.T) {
	resolve := func(cmd string, args []string) []string {
		if cmd == "task" && len(args) == 1 {
			return []string{"01J8-aaaa", "01J8-bbbb"}
		}
		if cmd == "resume" && len(args) == 1 {
			return []string{"sess-42"}
		}
		return nil
	}
	cases := []struct {
		in        string
		want      string
		completed bool
	}{
		// One candidate: completed in place with a trailing space.
		{"/resume se", "/resume sess-42 ", true},
		// Several: the common prefix, and no space (still choosing).
		{"/task 01", "/task 01J8-", true},
		// Nothing fits: the line is left exactly as typed.
		{"/task zz", "/task zz", false},
		// No argument yet: the command-name path still runs.
		{"/resum", "/resume ", true},
	}
	for _, tc := range cases {
		got, completed := tabCompleteAt([]rune(tc.in), []string{"/resume", "/task"}, resolve)
		if string(got) != tc.want || completed != tc.completed {
			t.Errorf("tabCompleteAt(%q) = %q,%v; want %q,%v", tc.in, string(got), completed, tc.want, tc.completed)
		}
	}
}
