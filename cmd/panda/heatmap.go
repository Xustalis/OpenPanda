package main

// Task-activity heatmap — a GitHub-contributions-style grid over the task
// store's created_at column. One column per week (Monday on top), one cell per
// day, cell intensity = how many tasks entered the system that day.
//
// The renderer is shared by every surface that can reach a task store:
// the TUI/classic-REPL welcome banner (the empty screen a session opens on),
// /heatmap as a slash command (committed as a transcript block), and
// `panda heatmap` for scripts. Colour comes from the shared palette —
// NO_COLOR and dumb terminals get a glyph ramp instead of escape sequences,
// and a non-unicode console gets ASCII cells.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// The window is always one year — 52 full weeks plus the current partial one —
// because that is the shape people read a heatmap for (the GitHub profile).
// heatmapMinWeek is the floor a very narrow terminal may fall back to before
// the grid stops reading as a grid at all.
const (
	heatmapMinWeek = 8
	heatmapMaxWeek = 53
)

// heatColors is the 256-colour green ramp behind the cells: index 0 is the
// "empty" square (near-black slate, GitHub dark's #161b22), 1–4 run dark→mint
// toward the brand green. Palette.SGR is the escape hatch for exactly this —
// a code already in hand — and returns the bare glyph when colour is off.
var heatColors = []string{"38;5;235", "38;5;22", "38;5;28", "38;5;35", "38;5;42"}

// heatRampUni / heatRampASCII are the monochrome intensity ramps. Colour
// terminals draw every level as a solid block and let the tint speak; without
// colour the glyph itself has to carry the intensity. The unicode ramp is a
// rising bar (▂→█) rather than the dithered ░▒▓ — same "more ink" reading
// without the static-noise texture.
var (
	heatRampUni   = []string{"·", "▂", "▄", "▆", "█"}
	heatRampASCII = []string{".", ":", "+", "*", "#"}
)

// heatmapFit picks the cell stride and the week count that fit width columns
// around a gutter-wide row label. A year of two-column cells (glyph + gap,
// the GitHub look) needs ~110 columns; narrower terminals drop the gap and
// pack the year into ~57, and only when even that cannot fit does the window
// itself shrink — a heatmap that stops showing the year stops answering the
// question it exists for.
func heatmapFit(width, gutter, weeks int) (cellW, fitted int) {
	if width <= 0 {
		width = listFallbackWidth
	}
	weeks = min(max(weeks, 1), heatmapMaxWeek)
	cellW = 2
	if gutter+weeks*cellW > width {
		cellW = 1
	}
	if fit := (width - gutter) / cellW; fit < weeks {
		weeks = max(fit, heatmapMinWeek)
	}
	return cellW, weeks
}

// heatLevel buckets a day's count into 0–4 relative to the busiest day in the
// window. Proportional rather than fixed thresholds: a node whose best day is
// 3 tasks still gets a visible ramp instead of a wall of level-1 cells.
func heatLevel(count, max int) int {
	if count <= 0 || max <= 0 {
		return 0
	}
	return min(int(math.Ceil(4*float64(count)/float64(max))), 4)
}

// heatCell renders one day's glyph. Colour + unicode gets the GitHub look (a
// solid square tinted by level); everywhere else the intensity rides on the
// glyph ramp. The stride — the space between columns — belongs to the caller.
func heatCell(p cliui.Palette, level int) string {
	ramp := heatRampASCII
	if p.Unicode() {
		ramp = heatRampUni
	}
	glyph := ramp[level]
	if p.Enabled() && p.Unicode() {
		glyph = "█"
	}
	return p.SGR(heatColors[level], glyph)
}

// renderTaskHeatmap writes the full view to w: a muted title, the month label
// row, the 7×weeks day grid, a less→more legend, and a summary line. today is
// a parameter so tests can pin the window; weeks is the requested span (a year
// by default); width is the column budget that heatmapFit lays the grid into.
func renderTaskHeatmap(w io.Writer, loc i18n.Locale, counts map[string]int, today time.Time, weeks, width int, p cliui.Palette) {
	if width <= 0 {
		width = listFallbackWidth
	}
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)

	// Weekday labels sit on the Mon/Wed/Fri rows, like GitHub's. The gutter is
	// sized to the widest label actually in use so CJK abbreviations don't
	// shift the grid left or ASCII ones leave it stranded.
	dowLabels := map[int]string{
		0: i18n.T(loc, "cli.heatmap.dow.mon"),
		2: i18n.T(loc, "cli.heatmap.dow.wed"),
		4: i18n.T(loc, "cli.heatmap.dow.fri"),
	}
	gutter := 1
	for _, l := range dowLabels {
		if n := cliui.DisplayWidth(l); n > gutter {
			gutter = n
		}
	}
	gutter++ // one space between the label and the first cell

	cellW, weeks := heatmapFit(width, gutter, weeks)

	// Columns start on Monday (ISO). The leftmost column is the Monday of the
	// week that is `weeks-1` weeks before the current one.
	offset := (int(day.Weekday()) + 6) % 7 // Mon=0 … Sun=6
	first := day.AddDate(0, 0, -offset-7*(weeks-1))
	at := func(col, dow int) time.Time { return first.AddDate(0, 0, 7*col+dow) }

	max, total, activeDays := 0, 0, 0
	for col := 0; col < weeks; col++ {
		for dow := 0; dow < 7; dow++ {
			d := at(col, dow)
			if d.After(day) {
				continue
			}
			n := counts[d.Format("2006-01-02")]
			total += n
			if n > 0 {
				activeDays++
			}
			if n > max {
				max = n
			}
		}
	}

	head := i18n.Tf(loc, "cli.heatmap.head", "weeks", strconv.Itoa(weeks))
	if weeks >= 52 {
		head = i18n.T(loc, "cli.heatmap.year")
	}
	fmt.Fprintln(w, p.Heading(head))

	// Month label row: a column earns its month's abbreviation when that week's
	// Monday opens a new month, and the label lands exactly over that column —
	// pos tracks where the row's pen is, so padding is measured from reality
	// rather than from where the previous label was allowed to end. A Monday
	// too close to the previous label is passed over so a later week of the
	// same month can still claim the name (the way GitHub slides "Feb" right
	// when "Jan" ran long); the month only loses its label when every one of
	// its Mondays is crowded out.
	var lb strings.Builder
	lb.WriteString(strings.Repeat(" ", gutter))
	pos, prevMonth := 0, -1
	for col := 0; col < weeks; col++ {
		m := int(at(col, 0).Month())
		if m == prevMonth {
			continue
		}
		start := col * cellW
		if pos > 0 && start < pos+1 {
			continue // would touch the previous label — retry on the next Monday
		}
		prevMonth = m
		label := i18n.T(loc, "cli.heatmap.month."+strconv.Itoa(m))
		lb.WriteString(strings.Repeat(" ", start-pos) + label)
		pos = start + cliui.DisplayWidth(label)
	}
	fmt.Fprintln(w, p.Muted(strings.TrimRight(lb.String(), " ")))

	for dow := 0; dow < 7; dow++ {
		var row strings.Builder
		if l, ok := dowLabels[dow]; ok {
			row.WriteString(l + strings.Repeat(" ", gutter-cliui.DisplayWidth(l)))
		} else {
			row.WriteString(strings.Repeat(" ", gutter))
		}
		for col := 0; col < weeks; col++ {
			d := at(col, dow)
			if d.After(day) {
				row.WriteString(strings.Repeat(" ", cellW)) // the future stays blank
				continue
			}
			row.WriteString(heatCell(p, heatLevel(counts[d.Format("2006-01-02")], max)))
			row.WriteString(strings.Repeat(" ", cellW-1))
		}
		fmt.Fprintln(w, strings.TrimRight(row.String(), " "))
	}

	// Legend + summary share the last line: the totals sit under the row
	// labels and the Less→More ramp docks at the grid's right edge, the way
	// GitHub parks it in the grid's own corner. The dimming goes on the words
	// only — a cell's own reset would end a Muted span early. When the line
	// cannot fit both, the legend falls back to its own row.
	less, more := i18n.T(loc, "cli.heatmap.less"), i18n.T(loc, "cli.heatmap.more")
	var legend strings.Builder
	legend.WriteString(p.Muted(less) + " ")
	for l := 0; l <= 4; l++ {
		legend.WriteString(heatCell(p, l) + " ")
	}
	legend.WriteString(p.Muted(more))
	legendW := cliui.DisplayWidth(less) + 1 + 10 + cliui.DisplayWidth(more)

	summary := i18n.Tf(loc, "cli.heatmap.summary",
		"total", strconv.Itoa(total),
		"days", strconv.Itoa(activeDays),
		"peak", strconv.Itoa(max),
		"streak", strconv.Itoa(heatStreak(counts, day)),
	)
	pad := width - cliui.DisplayWidth(summary) - legendW
	if pad >= 1 {
		fmt.Fprintln(w, p.Muted(summary)+strings.Repeat(" ", pad)+legend.String())
	} else {
		fmt.Fprintln(w, p.Muted(summary))
		fmt.Fprintln(w, strings.Repeat(" ", gutter)+legend.String())
	}
}

// heatStreak counts consecutive active days ending today — or yesterday when
// today is still empty, so a quiet morning doesn't read as a broken streak.
func heatStreak(counts map[string]int, today time.Time) int {
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	if counts[d.Format("2006-01-02")] == 0 {
		d = d.AddDate(0, 0, -1)
	}
	streak := 0
	for counts[d.Format("2006-01-02")] > 0 {
		streak++
		d = d.AddDate(0, 0, -1)
	}
	return streak
}

// activityCounts fetches the per-day task counts behind the welcome heatmap
// and /heatmap. A nil store (tests, a bare repl) or a failed query yields nil,
// which the banner treats as "no section" rather than "empty grid".
func (r *repl) activityCounts() map[string]int {
	if r == nil || r.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := r.store.TaskActivityByDay(ctx)
	if err != nil {
		return nil
	}
	return counts
}

// activityTTL is how long a fetched activity map stays valid. The welcome
// banner renders inside View — a value method Bubble Tea calls on every
// frame — so the store read cannot live in the render path; this cache gives
// it a once-per-half-minute budget, which keeps /clear honest after tasks
// moved without paying a query per keystroke.
const activityTTL = 30 * time.Second

// activityCache memoizes the heatmap's per-day counts behind a pointer every
// value-copy of the model shares — the same trick scrollLimit uses. Bubble
// Tea's update loop is single-goroutine, so no lock is needed.
type activityCache struct {
	counts  map[string]int
	fetched time.Time
}

// get returns the cached counts, refetching once per TTL. A failed fetch
// keeps the previous map (nil included — "no section" is also a cached
// answer) and still stamps the cache, so a sick store is retried per TTL
// rather than per frame. A nil receiver (a zero-value model in a test) yields
// nil.
func (c *activityCache) get(r *repl) map[string]int {
	if c == nil || r == nil {
		return nil
	}
	if !c.fetched.IsZero() && time.Since(c.fetched) < activityTTL {
		return c.counts
	}
	c.fetched = time.Now()
	if counts := r.activityCounts(); counts != nil {
		c.counts = counts
	}
	return c.counts
}

// cmdHeatmap implements "/heatmap [weeks]" — the activity grid as a transcript
// block. A bare numeric argument narrows the window; the default is the year.
func (r *repl) cmdHeatmap(arg string) {
	weeks := 0
	for _, f := range strings.Fields(arg) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 {
			weeks = n
		}
	}
	if weeks <= 0 {
		weeks = heatmapMaxWeek
	}

	if r.store == nil {
		r.outln(i18n.Tf(r.loc, "repl.err", "err", "task store unavailable"))
		return
	}
	counts, err := r.store.TaskActivityByDay(r.commandContext())
	if err != nil {
		r.storeErr(err)
		return
	}
	renderTaskHeatmap(r.commandOutput(), r.loc, counts, time.Now(), weeks, listWidth(), pal())
}

// runHeatmap implements `panda heatmap [--weeks N]` — the same grid as a
// one-shot panel command, so scripts and non-interactive shells get it too.
func runHeatmap(args []string) {
	fs := flag.NewFlagSet("heatmap", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	weeks := fs.Int("weeks", 0, "weeks of history to show (default: one year)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	counts, err := store.TaskActivityByDay(context.Background())
	if err != nil {
		fatal("query activity", err)
	}
	if jsonOutput {
		emitJSON(counts)
		return
	}

	n := *weeks
	if n <= 0 {
		n = heatmapMaxWeek
	}
	renderTaskHeatmap(os.Stdout, i18n.Detect(), counts, time.Now(), n, listWidth(), pal())
}
