//go:build !lite

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestHeatLevel pins the bucketing rule: empty days are level 0, the busiest
// day in the window is always the top level, and everything else scales
// proportionally between them.
func TestHeatLevel(t *testing.T) {
	cases := []struct {
		count, max, want int
	}{
		{0, 10, 0},
		{10, 10, 4}, // the peak day owns the top level
		{1, 4, 1},   // any activity beats the empty square
		{4, 4, 4},
		{1, 1, 4}, // a single-task history still gets a visible top cell
		{0, 0, 0},
		{5, 0, 0},
	}
	for _, tc := range cases {
		if got := heatLevel(tc.count, tc.max); got != tc.want {
			t.Errorf("heatLevel(%d, %d) = %d, want %d", tc.count, tc.max, got, tc.want)
		}
	}
	// Monotonic: within one window, more tasks must never render colder.
	prev := 0
	for c := 0; c <= 20; c++ {
		if l := heatLevel(c, 20); l < prev {
			t.Fatalf("heatLevel(%d, 20) = %d dropped below previous level %d", c, l, prev)
		} else {
			prev = l
		}
	}
}

// TestHeatmapFit covers the layout trade-off: a year is always the goal, the
// gapped two-column stride survives only where it fits, packed single columns
// carry the year into ~60-column terminals, and only below that does the
// window itself shrink — never below the floor.
func TestHeatmapFit(t *testing.T) {
	cases := []struct {
		width, gutter, weeks int
		wantW, wantWeeks     int
	}{
		{120, 4, 53, 2, 53}, // roomy terminal: gapped cells, full year
		{110, 4, 53, 2, 53}, // exactly at the gapped-year boundary
		{80, 4, 53, 1, 53},  // standard terminal: packed cells, still a year
		{57, 4, 53, 1, 53},  // the packed-year boundary
		{40, 4, 53, 1, 36},  // narrow: the window shrinks to fit
		{10, 4, 53, 1, 8},   // hopeless: the floor wins
		{0, 4, 53, 1, 53},   // unknown width falls back to 100 columns
		{80, 4, 12, 2, 12},  // an explicit small window keeps the gap
		{80, 4, 200, 1, 53}, // absurd requests clamp to the year
	}
	for _, tc := range cases {
		w, weeks := heatmapFit(tc.width, tc.gutter, tc.weeks)
		if w != tc.wantW || weeks != tc.wantWeeks {
			t.Errorf("heatmapFit(%d, %d, %d) = (%d, %d), want (%d, %d)",
				tc.width, tc.gutter, tc.weeks, w, weeks, tc.wantW, tc.wantWeeks)
		}
	}
}

// TestRenderTaskHeatmapShape renders the plain (no colour, ASCII) grid against
// a pinned "today" and checks the frame: header, month row, seven day rows in
// order, legend, summary — and that a busy day lands on the right row.
func TestRenderTaskHeatmapShape(t *testing.T) {
	loc := i18n.English
	p := cliui.Plain()
	// A Wednesday so the week has both past and future cells.
	today := time.Date(2026, 9, 23, 15, 0, 0, 0, time.Local) // Wed
	if today.Weekday() != time.Wednesday {
		t.Fatalf("fixture date is not a Wednesday: %s", today.Weekday())
	}
	// One hot day (today) and one warm day a week ago.
	counts := map[string]int{
		"2026-09-23": 8,
		"2026-09-16": 2,
	}

	var sb strings.Builder
	renderTaskHeatmap(&sb, loc, counts, today, 8, 120, p)
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")

	// head + months + 7 day rows + summary/legend
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10:\n%s", len(lines), sb.String())
	}
	if !strings.Contains(lines[0], "8 weeks") {
		t.Errorf("head line lost its span: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "Mon") || !strings.HasPrefix(lines[4], "Wed") || !strings.HasPrefix(lines[6], "Fri") {
		t.Errorf("weekday labels drifted:\n%s", strings.Join(lines[2:9], "\n"))
	}
	// The Wednesday row's last cell is today — the busiest day in the window,
	// so it must be the top ASCII glyph "#".
	wedRow := lines[4]
	if !strings.HasSuffix(wedRow, "#") {
		t.Errorf("today's cell should be the hottest glyph, row: %q", wedRow)
	}
	// One week earlier, same weekday: level 1 (2 of 8) — the ":" cell, sitting
	// one column before today's.
	if !strings.HasSuffix(strings.TrimRight(wedRow[:len(wedRow)-2], " "), ":") {
		t.Errorf("last week's Wednesday should be a warm cell, row: %q", wedRow)
	}
	// Friday is in the future this week: its row must end before today's column.
	friRow := lines[6]
	if strings.HasSuffix(friRow, "#") || cliui.DisplayWidth(friRow) >= cliui.DisplayWidth(wedRow) {
		t.Errorf("future days must stay blank — Fri row %q vs Wed row %q", friRow, wedRow)
	}
	// The summary line carries the totals and the docked Less→More legend.
	if !strings.Contains(lines[9], "10 tasks") || !strings.Contains(lines[9], "peak 8/day") {
		t.Errorf("summary lost the totals: %q", lines[9])
	}
	if !strings.Contains(lines[9], "less") || !strings.Contains(lines[9], "more") {
		t.Errorf("legend missing from summary line: %q", lines[9])
	}
}

// TestRenderTaskHeatmapEmptyWindow: a store with no tasks still draws the
// frame — an all-empty grid tells the truth better than a missing one.
func TestRenderTaskHeatmapEmptyWindow(t *testing.T) {
	var sb strings.Builder
	renderTaskHeatmap(&sb, i18n.English, map[string]int{},
		time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local), 8, 120, cliui.Plain())
	out := sb.String()
	if !strings.Contains(out, "0 tasks") {
		t.Fatalf("empty window should report 0 tasks:\n%s", out)
	}
	if !strings.Contains(out, "Mon") {
		t.Fatalf("empty window lost the grid:\n%s", out)
	}
}

// TestHeatStreak covers the consecutive-day counter, including the
// quiet-morning rule (today empty → the streak counts back from yesterday).
func TestHeatStreak(t *testing.T) {
	today := time.Date(2026, 9, 23, 9, 0, 0, 0, time.Local)
	counts := map[string]int{
		"2026-09-22": 1,
		"2026-09-21": 3,
		"2026-09-20": 1,
		"2026-09-18": 9, // gap on the 19th ends the run
	}
	if got := heatStreak(counts, today); got != 3 {
		t.Fatalf("streak = %d, want 3 (20th→22nd, today still empty)", got)
	}
	counts["2026-09-23"] = 1 // today now counts, extending the streak
	if got := heatStreak(counts, today); got != 4 {
		t.Fatalf("streak = %d, want 4 once today has activity", got)
	}
	if got := heatStreak(map[string]int{}, today); got != 0 {
		t.Fatalf("empty history streak = %d, want 0", got)
	}
}

// newActivityStore builds a real in-memory task store with one task created
// "now" — enough rows for the heatmap to light today's cell.
func newActivityStore(t *testing.T) *core.TaskStore {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := core.NewTaskStore(db, nil)
	if _, err := st.Create(context.Background(), "", "", "seed", "node-a", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	return st
}

// TestWelcomeShowsYearHeatmap pins the feature on the entry screen: with a
// reachable store the banner carries a full year of activity — the head line,
// the grid, today's lit cell — and every row still respects the terminal.
func TestWelcomeShowsYearHeatmap(t *testing.T) {
	r := &repl{loc: i18n.English, cfg: &config.Config{}, store: newActivityStore(t), interactive: true}
	m := newTUIModel(r)
	m = step(m, tea.WindowSizeMsg{Width: 80, Height: 40})

	w := m.welcome()
	if !strings.Contains(w, "task activity") || !strings.Contains(w, "last year") {
		t.Fatalf("welcome banner lost the year heatmap:\n%s", w)
	}
	if !strings.Contains(w, "peak 1/day") {
		t.Fatalf("welcome heatmap lost the seeded task:\n%s", w)
	}
	for _, line := range strings.Split(w, "\n") {
		if n := cliui.DisplayWidth(line); n > 80 {
			t.Fatalf("banner line overflows 80 columns (%d): %q", n, line)
		}
	}
}

// TestActivityCacheGet covers the memoization rules the banner depends on:
// nil cache or nil store → no section, a healthy store → the map, and the
// answer (even an empty one) is held for the TTL rather than re-queried.
func TestActivityCacheGet(t *testing.T) {
	var nilCache *activityCache
	if got := nilCache.get(&repl{}); got != nil {
		t.Fatalf("nil cache returned %v", got)
	}

	c := &activityCache{}
	if got := c.get(&repl{}); got != nil {
		t.Fatalf("nil store returned %v", got)
	}
	if c.fetched.IsZero() {
		t.Fatal("a nil answer should still stamp the cache — otherwise every frame retries")
	}

	st := newActivityStore(t)
	r := &repl{store: st}
	c2 := &activityCache{}
	if got := c2.get(r); len(got) != 1 {
		t.Fatalf("store with one task returned %v", got)
	}
	// Within the TTL the same answer is served — even to a repl whose store
	// has since vanished, the cached map wins over a fresh query.
	if got := c2.get(&repl{}); len(got) != 1 {
		t.Fatalf("cache within TTL should serve the stored map, got %v", got)
	}
}
