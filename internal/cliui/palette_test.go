package cliui

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPaletteDisabledIsIdentity is the invariant every caller relies on: a
// palette that is off must return its input byte-for-byte, so feature code can
// style unconditionally instead of branching on stdoutIsTTY.
func TestPaletteDisabledIsIdentity(t *testing.T) {
	p := Plain()
	for _, s := range []string{"", "plain", "中文", "a\tb"} {
		for name, got := range map[string]string{
			"Bold":    p.Bold(s),
			"Dim":     p.Dim(s),
			"Accent":  p.Accent(s),
			"Danger":  p.Danger(s),
			"Heading": p.Heading(s),
			"Command": p.Command(s),
			"SGR":     p.SGR("1;33", s),
		} {
			if got != s {
				t.Errorf("%s(%q) = %q on a disabled palette, want the input unchanged", name, s, got)
			}
		}
	}
}

// TestColorEnabledPrecedence pins the environment contract: NO_COLOR beats
// everything (no-color.org — any value, including "0"), FORCE_COLOR beats the
// TTY check, and TERM=dumb wins over an actual terminal.
func TestColorEnabledPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		want bool
	}{
		{"tty", nil, true, true},
		{"not a tty", nil, false, false},
		{"NO_COLOR beats a tty", map[string]string{"NO_COLOR": ""}, true, false},
		{"NO_COLOR=0 still disables", map[string]string{"NO_COLOR": "0"}, true, false},
		{"NO_COLOR beats FORCE_COLOR", map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, false, false},
		{"FORCE_COLOR without a tty", map[string]string{"FORCE_COLOR": "1"}, false, true},
		{"FORCE_COLOR=0 disables", map[string]string{"FORCE_COLOR": "0"}, true, false},
		{"CLICOLOR_FORCE without a tty", map[string]string{"CLICOLOR_FORCE": "1"}, false, true},
		{"CLICOLOR=0 disables", map[string]string{"CLICOLOR": "0"}, true, false},
		{"TERM=dumb disables", map[string]string{"TERM": "dumb"}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A clean slate: the developer's own shell must not decide the test.
			clearEnv(t, "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "COLORTERM")
			t.Setenv("TERM", "xterm-256color")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := colorEnabled(c.tty); got != c.want {
				t.Errorf("colorEnabled(tty=%v) = %v, want %v", c.tty, got, c.want)
			}
		})
	}
}

// clearEnv truly unsets keys for the duration of the test: t.Setenv first so the
// original value is registered for restore, then os.Unsetenv — "set to empty" is
// a different state from "unset" for NO_COLOR.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// TestGlyphFallback checks the bare-Linux-console path: no unicode means no
// glyph anywhere, because the VT font draws every non-ASCII rune as a diamond.
func TestGlyphFallback(t *testing.T) {
	ascii := New(false, false)
	for name, got := range map[string]string{
		"MarkOK":     ascii.MarkOK(),
		"MarkFail":   ascii.MarkFail(),
		"MarkBullet": ascii.MarkBullet(),
		"MarkArrow":  ascii.MarkArrow(),
		"Separator":  ascii.Separator(),
	} {
		for _, r := range got {
			if r > 0x7f {
				t.Errorf("%s() = %q, which is not ASCII", name, got)
				break
			}
		}
	}
	if got := Frames(ascii); len(got) == 0 || got[0] != ASCIIWheel[0] {
		t.Errorf("Frames on a non-unicode palette = %q, want the ASCII wheel", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                    "0ms",
		250 * time.Millisecond:               "250ms",
		1200 * time.Millisecond:              "1.2s",
		9900 * time.Millisecond:              "9.9s",
		42 * time.Second:                     "42s",
		90 * time.Second:                     "1m30s",
		3725 * time.Second:                   "62m05s",
		2*time.Minute + 500*time.Millisecond: "2m00s",
	}
	for in, want := range cases {
		if got := HumanDuration(in); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", in, got, want)
		}
	}
	// Whatever the input, the field must stay narrow enough for the status line.
	for _, d := range []time.Duration{0, time.Second, time.Hour, 100 * time.Hour} {
		if w := DisplayWidth(HumanDuration(d)); w > 8 {
			t.Errorf("HumanDuration(%v) is %d columns wide, want ≤ 8", d, w)
		}
	}
}

func TestHumanCount(t *testing.T) {
	cases := map[int64]string{
		-5: "0", 0: "0", 999: "999", 1000: "1k", 1234: "1.2k",
		12_000: "12k", 999_999: "1000k", 1_234_567: "1.2M",
	}
	for in, want := range cases {
		if got := HumanCount(in); got != want {
			t.Errorf("HumanCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestTruncateColumns is the geometry guard for the status line: the result must
// never exceed the column budget, even for double-width text, or the \r repaint
// would leave orphaned rows on screen.
func TestTruncateColumns(t *testing.T) {
	for _, in := range []string{"short", "a much longer status line than fits", "思考中的模型正在运行工具"} {
		for _, max := range []int{1, 2, 3, 5, 10, 20} {
			got := Truncate(in, max, true)
			if w := DisplayWidth(got); w > max {
				t.Errorf("Truncate(%q, %d) = %q (%d columns), want ≤ %d", in, max, got, w, max)
			}
		}
	}
	if got := Truncate("keep", 10, true); got != "keep" {
		t.Errorf("Truncate left a short string alone? got %q", got)
	}
	if got := Truncate("truncate me", 8, false); strings.ContainsRune(got, '…') {
		t.Errorf("non-unicode Truncate used an ellipsis rune: %q", got)
	}
}

// TestTruncateTailColumns is the same geometry guard from the other end: the
// tail variant feeds the streaming preview, so it must respect the budget and
// keep the END of the string.
func TestTruncateTailColumns(t *testing.T) {
	for _, in := range []string{"short", "a much longer status line than fits", "思考中的模型正在运行工具"} {
		for _, max := range []int{1, 2, 3, 5, 10, 20} {
			got := TruncateTail(in, max, true)
			if w := DisplayWidth(got); w > max {
				t.Errorf("TruncateTail(%q, %d) = %q (%d columns), want ≤ %d", in, max, got, w, max)
			}
		}
	}
	if got := TruncateTail("keep", 10, true); got != "keep" {
		t.Errorf("TruncateTail clipped a string that fits: %q", got)
	}
	if got := TruncateTail("truncate me", 8, true); !strings.HasSuffix(got, "ate me") {
		t.Errorf("TruncateTail dropped the tail: %q", got)
	}
	if got := TruncateTail("truncate me", 8, false); strings.ContainsRune(got, '…') {
		t.Errorf("non-unicode TruncateTail used an ellipsis rune: %q", got)
	}
}

// TestDisplayWidthCJK keeps the shared width table honest — the line editor
// depends on the same numbers for its wrap math.
func TestDisplayWidthCJK(t *testing.T) {
	cases := map[string]int{"": 0, "abc": 3, "中文": 4, "中a": 3, "\x01": 0, "é": 1}
	for in, want := range cases {
		if got := DisplayWidth(in); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}
