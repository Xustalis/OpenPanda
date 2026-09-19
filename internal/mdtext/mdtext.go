// Package mdtext converts light Markdown into the two forms OpenPanda's
// surfaces need beyond a rendered web page:
//
//   - Plain: readable prose for TTS (the voice pipeline) and for pipes /
//     logs — emphasis markers, link syntax and fence markers are dropped,
//     table rows become comma-separated values.
//   - ANSI: a terminal-friendly render for TTYs — headings become bold
//     cyan, bold/italic/code keep their emphasis via SGR, tables stay
//     column-aligned.
//
// It is a small line-oriented state machine, not a full Markdown parser:
// model answers are light Markdown (headings, lists, tables, fenced code,
// emphasis), and the goal is that nothing reads worse than it would as raw
// text. A gofmt-style fenced block passes through untouched (plain) or dim
// (ANSI); anything unrecognized is kept verbatim.
package mdtext

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"golang.org/x/term"
)

// inline patterns shared by both renderers. Ordering matters when applying:
// links and bold before italic, code spans independently.
var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBold     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic   = regexp.MustCompile(`(?m)\*([^*\n]+)\*`)
	reCode     = regexp.MustCompile("`([^`\n]+)`")
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reRule     = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	reTable    = regexp.MustCompile(`^\s*\|(.+)\|\s*$`)
	reTableSep = regexp.MustCompile(`^\s*\|?[\s:-]+\|[\s:|-]*$`)
	reListItem = regexp.MustCompile(`^(\s*)[-*•]\s+`)
	reNumItem  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+`)
	reQuote    = regexp.MustCompile(`^(\s*)>\s?`)
)

// IsFenceStart reports whether a line opens or closes a fenced code block
// (``` markers); streaming line renderers track fence state with this and
// pass lines through untouched inside a fence.
func IsFenceStart(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// LineANSI renders one Markdown line with SGR emphasis (see ANSI). fence
// selects code-block pass-through. It is stateless so a stream can render
// line by line as deltas arrive.
func LineANSI(line string, fence bool) string {
	if fence {
		return "\x1b[2m" + line + "\x1b[0m"
	}
	return ansiLine(line)
}

// LinePlain renders one Markdown line as plain text (see Plain). fence
// selects code-block pass-through.
func LinePlain(line string, fence bool) string {
	if fence {
		return line
	}
	return plainLine(line)
}

// Plain strips Markdown syntax for speech and plain-text sinks: headings
// keep their text, emphasis and link markers drop out, fenced code stays
// verbatim, tables flatten to comma-separated cells, list bullets become
// "• " and horizontal rules become "—".
func Plain(s string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue // the fence marker itself is never spoken
		}
		if inFence {
			b.WriteString(line + "\n")
			continue
		}
		b.WriteString(plainLine(line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func plainLine(line string) string {
	if reRule.MatchString(line) {
		return "—"
	}
	if m := reHeading.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[2])
	}
	if reTableSep.MatchString(line) {
		return "" // |---|---| separator: pure layout
	}
	if m := reTable.FindStringSubmatch(line); m != nil {
		cells := splitCells(m[1])
		// Inline markers inside cells (**bold**, `code`, links) must strip
		// too — a table row flattened with raw stars reads as noise.
		for i, c := range cells {
			cells[i] = inlinePlain(c)
		}
		return strings.Join(cells, ", ")
	}
	line = reQuote.ReplaceAllString(line, "$1")
	line = reListItem.ReplaceAllString(line, "$1• ")
	line = reNumItem.ReplaceAllString(line, "$1$2. ")
	return inlinePlain(line)
}

// inlinePlain strips inline emphasis/link/code markers from a text span.
func inlinePlain(s string) string {
	s = reLink.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	s = reCode.ReplaceAllString(s, "$1")
	return strings.TrimRight(s, " \t")
}

// ANSI renders Markdown with SGR emphasis for color TTYs: headings bold
// cyan, fenced code dim, bold→bold, code→dim, italic→italic, tables
// column-aligned, lists and quotes kept. When the caller already knows the
// sink is not a TTY it should use Plain instead.
func ANSI(s string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			b.WriteString("\x1b[2m" + line + "\x1b[0m\n")
			continue
		}
		b.WriteString(ansiLine(line) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func ansiLine(line string) string {
	if reRule.MatchString(line) {
		return "\x1b[2m————————————————\x1b[0m"
	}
	if m := reHeading.FindStringSubmatch(line); m != nil {
		return "\x1b[1;36m" + inlineANSI(strings.TrimSpace(m[2])) + "\x1b[0m"
	}
	if reTableSep.MatchString(line) {
		return "" // alignment row: the ANSI table renders its own
	}
	if m := reTable.FindStringSubmatch(line); m != nil {
		return tableANSI(m[1])
	}
	if m := reQuote.FindStringSubmatch(line); m != nil {
		return "\x1b[2m│ " + inlineANSI(strings.TrimLeft(m[0], " \t>")) + "\x1b[0m"
	}
	return inlineANSI(reListItem.ReplaceAllString(line, "$1• "))
}

// inlineANSI applies inline emphasis to one non-table line.
func inlineANSI(line string) string {
	line = reLink.ReplaceAllString(line, "$1")
	line = reBold.ReplaceAllString(line, "\x1b[1m$1\x1b[22m")
	line = reItalic.ReplaceAllString(line, "\x1b[3m$1\x1b[23m")
	line = reCode.ReplaceAllString(line, "\x1b[2m$1\x1b[0m")
	return strings.TrimRight(line, " \t")
}

// splitCells splits one table line's inner cells on "|" boundaries that are
// not escaped; surrounding spaces of each cell are trimmed.
func splitCells(inner string) []string {
	parts := strings.Split(inner, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

// tableANSI pads cells to a per-call column width and rejoins with " │ ".
// Column widths cannot be tracked across lines (the renderer is stateless
// per line), so alignment is computed from the widest cell *in this line* —
// enough to keep the row readable while staying a pure function.
func tableANSI(inner string) string {
	cells := splitCells(inner)
	w := 0
	for _, c := range cells {
		if n := utf8.RuneCountInString(stripInline(c)); n > w {
			w = n
		}
	}
	parts := make([]string, len(cells))
	for i, c := range cells {
		pad := w - utf8.RuneCountInString(stripInline(c))
		parts[i] = inlineANSI(c) + strings.Repeat(" ", pad)
	}
	return " " + strings.Join(parts, " │ ")
}

// stripInline removes emphasis/link markers so padding math counts visible
// runes, not the SGR-wrapped output of inlineANSI.
func stripInline(s string) string {
	s = reLink.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	s = reCode.ReplaceAllString(s, "$1")
	return s
}

// isDarkBackground reports whether the terminal is expected to have a dark
// background without querying the terminal via escape sequences (OSC 11), which
// would leak into stdin during raw-mode TUI input loops.
func isDarkBackground() bool {
	if s := os.Getenv("COLORFGBG"); s != "" {
		parts := strings.Split(s, ";")
		if len(parts) >= 2 {
			bg, err := strconv.Atoi(parts[len(parts)-1])
			if err == nil {
				return bg < 7 || bg == 8
			}
		}
	}
	return true
}

func defaultStyle() ansi.StyleConfig {
	var cfg ansi.StyleConfig
	if isDarkBackground() {
		cfg = styles.DarkStyleConfig
	} else {
		cfg = styles.LightStyleConfig
	}
	zero := uint(0)
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix = ""
	cfg.Document.BlockSuffix = ""
	return cfg
}

// Render formats s into rich terminal Markdown using Glamour with automatic
// dark/light styling and word wrap to width columns. If width <= 0, word wrap
// is disabled. If Glamour fails or cannot format, it gracefully falls back to ANSI(s).
func Render(s string, width int) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	var opts []glamour.TermRendererOption
	opts = append(opts, glamour.WithStyles(defaultStyle()))
	if width > 0 {
		opts = append(opts, glamour.WithWordWrap(width))
	}
	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return ANSI(s), err
	}
	out, err := r.Render(s)
	if err != nil {
		return ANSI(s), err
	}
	return strings.Trim(out, "\n"), nil
}

// RenderTerminal formats s for terminal display. If NO_COLOR is set, it falls
// back to Plain(s). Otherwise it renders via Glamour with word wrapping fitted
// to the terminal width (default 80, capped at 120 cols for reading comfort).
func RenderTerminal(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if os.Getenv("NO_COLOR") != "" {
		return Plain(s)
	}

	width := 80
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			width = w
		}
	}
	if width > 120 {
		width = 120
	} else if width > 4 {
		width -= 2
	}

	out, err := Render(s, width)
	if err != nil {
		return ANSI(s)
	}
	return out
}
