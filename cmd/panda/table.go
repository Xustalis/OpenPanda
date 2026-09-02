package main

// Width-aware column layout for the CLI's list commands (`status`, `queue`,
// `session list`, `metrics`). Before this existed each list printed its own
// `fmt.Printf("%-36s %-12s …")` line, which broke in three ways the moment real
// data arrived: `%-Ns` pads by *bytes*, so a CJK title (two columns per rune)
// pushed every later column out of alignment; a tinted cell counted its escape
// bytes as content; and nothing measured the terminal, so a long row wrapped and
// turned one task into two ragged lines.
//
// Everything here measures in display columns via cliui.DisplayWidth, which is
// the same measure the status line and the line editor use — one notion of
// "how wide is this" for the whole CLI.

import (
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// listFallbackWidth is the assumed terminal width when the ioctl cannot answer
// (piped output, a platform without TIOCGWINSZ). 100 columns is wide enough for
// a task row to keep its title and narrow enough to stay inside a default
// terminal, so a redirected listing and an on-screen one read the same.
const listFallbackWidth = 100

// listWidth is the column budget one row may fill. Re-read per listing so a
// resize between two commands is honoured.
func listWidth() int {
	if n := termColumns(); n > 0 {
		return n
	}
	return listFallbackWidth
}

// cell fits s into exactly n display columns: truncated with an ellipsis when it
// overflows, space-padded when it falls short. Pass unstyled text — the padding
// is computed from what the terminal will actually show.
func cell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = cliui.Truncate(s, n, pal().Unicode())
	if pad := n - cliui.DisplayWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// styledCell is cell for a tinted column. It measures the plain text, then wraps
// the clipped result in style, so the escape bytes never enter the padding
// arithmetic — the bug that used to knock every column right of a coloured state
// word out of line on a colour terminal.
func styledCell(s string, n int, style func(string) string) string {
	if n <= 0 {
		return ""
	}
	s = cliui.Truncate(s, n, pal().Unicode())
	pad := n - cliui.DisplayWidth(s)
	if pad < 0 {
		pad = 0
	}
	return style(s) + strings.Repeat(" ", pad)
}

// row joins pre-sized cells with a single space and trims the trailing padding,
// so a listing never emits invisible whitespace to the end of the line (which
// makes a copy-pasted row carry junk, and a `--json`-less diff noisy).
func row(cells ...string) string {
	return strings.TrimRight(strings.Join(cells, " "), " ")
}

// listHeader dims a header row so the column names read as a label rather than
// as another record. On a monochrome terminal the dim is a no-op and the header
// still reads as the first line of the table.
func listHeader(cells ...string) string {
	return pal().Muted(row(cells...))
}

// humanAge phrases a unix timestamp as an age ("just now", "5m ago", "3d ago").
// A heartbeat's exact RFC3339 instant is precision nobody reads: the question a
// node list answers is "is this machine still there", and an age answers it in
// four columns instead of twenty-five.
func humanAge(loc i18n.Locale, unix int64) string {
	if unix <= 0 {
		return i18n.T(loc, "cli.age.never")
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	// A negative age is clock skew between two nodes, not the future: "now"
	// is the honest reading, and it keeps the column from printing "-3m ago".
	case d < time.Minute:
		return i18n.T(loc, "cli.age.now")
	case d < time.Hour:
		return i18n.Tf(loc, "cli.age.minutes", "n", strconv.Itoa(int(d.Minutes())))
	case d < 24*time.Hour:
		return i18n.Tf(loc, "cli.age.hours", "n", strconv.Itoa(int(d.Hours())))
	default:
		return i18n.Tf(loc, "cli.age.days", "n", strconv.Itoa(int(d.Hours())/24))
	}
}
