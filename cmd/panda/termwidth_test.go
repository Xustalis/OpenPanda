package main

import "testing"

// TestRuneWidth pins the compact width table the line editor's wrap math
// depends on: CJK and emoji render double-wide, ASCII and combining marks
// narrow/zero. A regression here desyncs the in-place menu rendering on
// Chinese input.
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
		if got := runeWidth(c.r); got != c.want {
			t.Errorf("runeWidth(%q U+%04X) = %d, want %d", c.r, c.r, got, c.want)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	if got := displayWidth("panda> 你好"); got != 7+4 {
		t.Errorf("displayWidth = %d, want %d", got, 7+4)
	}
}
