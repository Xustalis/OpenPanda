package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestCellMeasuresDisplayColumns is the bug the table helpers exist to prevent:
// %-Ns pads by bytes, so a CJK cell (two columns per rune, three bytes per rune)
// used to knock every column right of it out of alignment.
func TestCellMeasuresDisplayColumns(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
	}{
		{"ascii pads", "done", 10},
		{"cjk pads", "任务", 10},
		{"mixed pads", "task 任务", 12},
		{"exact fit", "done", 4},
	} {
		got := cell(tc.in, tc.n)
		if w := cliui.DisplayWidth(got); w != tc.n {
			t.Errorf("%s: cell(%q, %d) is %d columns wide, want %d", tc.name, tc.in, tc.n, w, tc.n)
		}
	}
}

// TestCellTruncatesInsteadOfOverflowing guards the row-wrapping failure: a cell
// wider than its column has to be clipped, or the row spills onto a second line
// and one record reads as two.
func TestCellTruncatesInsteadOfOverflowing(t *testing.T) {
	long := strings.Repeat("查看系统进程", 6) // 72 columns
	got := cell(long, 20)
	if w := cliui.DisplayWidth(got); w > 20 {
		t.Fatalf("cell overflowed its column: %d > 20 (%q)", w, got)
	}
	if got == long {
		t.Fatal("cell should have clipped a 72-column title")
	}
}

// TestRowTrimsTrailingPadding keeps a copy-pasted listing free of invisible
// whitespace: the last cell's padding is never printed.
func TestRowTrimsTrailingPadding(t *testing.T) {
	got := row(cell("a", 6), cell("b", 6))
	if strings.HasSuffix(got, " ") {
		t.Fatalf("row kept trailing padding: %q", got)
	}
	if got != "a      b" {
		t.Fatalf("row spacing changed: %q", got)
	}
}

// TestHumanAge covers the buckets a node listing actually shows.
func TestHumanAge(t *testing.T) {
	loc := i18n.English
	now := time.Now()
	cases := []struct {
		name string
		at   int64
		want string
	}{
		{"never", 0, "never"},
		{"seconds", now.Add(-10 * time.Second).Unix(), "just now"},
		{"clock skew", now.Add(90 * time.Second).Unix(), "just now"},
		{"minutes", now.Add(-5 * time.Minute).Unix(), "5m ago"},
		{"hours", now.Add(-3 * time.Hour).Unix(), "3h ago"},
		{"days", now.Add(-50 * time.Hour).Unix(), "2d ago"},
	}
	for _, tc := range cases {
		if got := humanAge(loc, tc.at); got != tc.want {
			t.Errorf("%s: humanAge = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestShortNodeTrimsOnlyRuntimeSuffixes: the 8-hex runtime suffix is router
// bookkeeping and costs nine columns of every row, but a hostname that merely
// ends in a dash-group must survive intact.
func TestShortNodeTrimsOnlyRuntimeSuffixes(t *testing.T) {
	cases := map[string]string{
		"":                                   "-",
		"mac-2a08d48f":                       "mac",
		"XenithdeMacBook-Pro.local-2e3a69dc": "XenithdeMacBook-Pro.local",
		"XenithdeMacBook-Pro.local":          "XenithdeMacBook-Pro.local",
		"node-orangepi":                      "node-orangepi", // not hex
		"node-2a08d48":                       "node-2a08d48",  // 7 digits, not the suffix
	}
	for in, want := range cases {
		if got := shortNode(in); got != want {
			t.Errorf("shortNode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPercentileNearestRank pins the summary statistic `panda metrics` reports.
func TestPercentileNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if got := percentile(sorted, 50); got != 60 {
		t.Errorf("p50 = %d, want 60", got)
	}
	if got := percentile(sorted, 95); got != 100 {
		t.Errorf("p95 = %d, want 100", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty p50 = %d, want 0", got)
	}
}
