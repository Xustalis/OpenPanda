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
	"regexp"
	"strings"
	"unicode/utf8"
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
