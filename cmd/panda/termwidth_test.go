package main

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/cliui"
)

// TestRuneWidth pins the compact width table the line editor's wrap math
// depends on: CJK and emoji render double-wide, ASCII and combining marks
// narrow/zero. A regression here desyncs the in-place menu rendering on
// Chinese input. The table itself lives in internal/cliui (the status line
// shares it); the unix-only term_unix.go wrappers are one-line pass-throughs,
// so the test targets cliui directly and runs on every platform.
func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1}, {'Z', 1}, {'0', 1}, {'/', 1}, {' ', 1},
		{'中', 2}, {'文', 2}, {'。', 2}, {'，', 2},
		{'あ', 2}, {'한', 2},
		{'😀', 2},    // U+1F600 emoji
		{0x0301, 0}, // combining acute accent
	}
	for _, c := range cases {
		if got := cliui.RuneWidth(c.r); got != c.want {
			t.Errorf("RuneWidth(%q U+%04X) = %d, want %d", c.r, c.r, got, c.want)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	if got := cliui.DisplayWidth("panda> 你好"); got != 7+4 {
		t.Errorf("DisplayWidth = %d, want %d", got, 7+4)
	}
}
