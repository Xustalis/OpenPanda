package main

// The CLI's shared presentation layer: one palette for the whole process and
// one constructor for the live status line.
//
// Every surface (banner, footer, help, watch boards, streaming answers) styles
// through pal(). It is resolved once — the TTY, TERM, NO_COLOR and locale facts
// it depends on cannot change inside a process — so the cost is a single
// ioctl-free env read no matter how many lines get printed.

import (
	"os"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// pal is the process-wide palette: colour when stdout is a terminal that wants
// it, unicode glyphs when the font can draw them.
var pal = sync.OnceValue(func() cliui.Palette {
	return cliui.New(stdoutIsTTY(), termSupportsUnicode())
})

// newStatusLine builds the live status line for one run. It animates only on a
// colour-capable interactive terminal; piped output and --json get the static
// one-line-per-event form instead, so logs stay greppable.
func newStatusLine(loc i18n.Locale) *cliui.Status {
	live := stdoutIsTTY() && !jsonOutput
	st := cliui.NewStatus(os.Stdout, pal(), live)
	st.SetTokenWord(i18n.T(loc, "cli.status.tokens"))
	st.SetWidth(termColumns)
	return st
}

// statusVerb is the word the spinner shows while an ask converges. Kept next to
// the constructor so every surface uses the same vocabulary.
func statusVerb(loc i18n.Locale) string { return i18n.T(loc, "cli.status.thinking") }

// thoughtPreview turns a stream of reasoning deltas into a one-line preview for
// the status line: it keeps only the tail after the last newline (the thought
// still being written) so a long chain-of-thought stays a single moving line
// rather than scrolling the terminal. It holds no full transcript — Phase 1
// reasoning is display-only (D14); the collapsible thought block is Phase 2.
type thoughtPreview struct {
	line strings.Builder
}

// feed consumes one reasoning delta and returns the current preview line, or ""
// when there is nothing printable yet. Newlines reset the line to the text that
// follows the last one.
func (t *thoughtPreview) feed(chunk string) string {
	if i := strings.LastIndexByte(chunk, '\n'); i >= 0 {
		t.line.Reset()
		t.line.WriteString(chunk[i+1:])
	} else {
		t.line.WriteString(chunk)
	}
	return strings.TrimSpace(t.line.String())
}

// progressNote phrases one engine progress event in the user's language. The
// engine reports these structured precisely so the wording lives here, next to
// every other user-facing string, instead of being composed in English inside
// a library.
func progressNote(loc i18n.Locale, p askengine.Progress) string {
	switch p.Kind {
	case askengine.ProgressTask:
		return i18n.Tf(loc, "cli.progress.task", "title", p.Name)
	case askengine.ProgressPlan:
		return i18n.Tf(loc, "cli.progress.plan", "goal", p.Name)
	case askengine.ProgressTool:
		return i18n.Tf(loc, "cli.progress.tool", "name", p.Name)
	case askengine.ProgressRoute:
		return i18n.Tf(loc, "cli.progress.route", "node", p.Name)
	case askengine.ProgressExec:
		return i18n.Tf(loc, "cli.progress.exec", "agent", p.Name)
	case askengine.ProgressJudge:
		return i18n.T(loc, "cli.progress.judge")
	}
	return p.Name
}

// palFor picks the palette for one stream. Usage text goes to stderr on a bad
// subcommand and stdout on `panda help`; a piped stdout must not decide whether
// stderr gets colour.
func palFor(f *os.File) cliui.Palette {
	if f == os.Stdout {
		return pal()
	}
	return cliui.New(fileIsTTY(f), termSupportsUnicode())
}

// fileIsTTY reports whether f is an interactive terminal.
func fileIsTTY(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// styleHelpLine tints one line of `panda help` so the page can be scanned
// instead of read: section headings stand out, and the typeable part of each
// entry (the subcommand and its flags) is tinted while the description stays
// plain text — descriptions are the bulk of the page, and dimming them would
// hurt exactly the people reading help in the first place.
//
// The layout is the parser: a heading ends in ":" at column 0, an entry is
// indented and separates command from description with two or more spaces, and
// a continuation line (indented, no command) is dimmed as a wrapped tail.
func styleHelpLine(p cliui.Palette, line string) string {
	if line == "" || !p.Enabled() {
		return line
	}
	if !strings.HasPrefix(line, " ") {
		if strings.HasSuffix(line, ":") {
			return p.Heading(line)
		}
		return p.Bold(line) // the title and the global-flags note
	}
	body := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(body)]
	cmd, rest, found := strings.Cut(body, "  ")
	if !found {
		return indent + p.Command(body)
	}
	if strings.TrimSpace(cmd) == "" {
		return p.Muted(line) // a wrapped description tail
	}
	return indent + p.Command(cmd) + "  " + rest
}
