package cliui

// Terminal geometry: how wide a rune renders. The line editor
// (cmd/panda/term_unix.go) and the status line both need this and used to carry
// separate copies; this is the single source of truth.

import "strings"

// RuneWidth approximates the terminal column count of r: East Asian and emoji
// ranges render double-wide, control and combining marks zero. A compact table
// — full wcwidth tables are overkill for a line editor.
func RuneWidth(r rune) int {
	if r < 0x20 {
		return 0
	}
	if r >= 0x0300 && r <= 0x036F { // combining diacritics
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals · Kangxi · CJK symbols
		r >= 0x3041 && r <= 0x33FF, // kana · CJK strokes · compat
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xA000 && r <= 0xA4CF, // Yi · Hangul syllable blocks start
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compat ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compat forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD: // CJK ext B+
		return 2
	}
	return 1
}

// DisplayWidth sums the column count of s. Escape sequences are NOT accounted
// for — pass unstyled text (the status line measures before it paints).
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

// Truncate clips s to at most max columns, marking the cut. It measures in
// columns rather than runes so a CJK status line cannot overflow into a second
// physical row — which would break the \r-based repaint of the status line.
func Truncate(s string, max int, unicode bool) string {
	if max <= 0 || DisplayWidth(s) <= max {
		return s
	}
	mark := "…"
	if !unicode {
		mark = "..."
	}
	budget := max - DisplayWidth(mark)
	if budget <= 0 {
		// No room even for the marker: hard-clip to the column budget.
		var b strings.Builder
		w := 0
		for _, r := range s {
			cw := RuneWidth(r)
			if w+cw > max {
				break
			}
			b.WriteRune(r)
			w += cw
		}
		return b.String()
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		cw := RuneWidth(r)
		if w+cw > budget {
			break
		}
		b.WriteRune(r)
		w += cw
	}
	return b.String() + mark
}

// TruncateTail is Truncate from the other end: it keeps the LAST max columns
// and marks the cut at the front. A live preview of text still arriving wants
// the newest words on screen, not the oldest.
func TruncateTail(s string, max int, unicode bool) string {
	if max <= 0 || DisplayWidth(s) <= max {
		return s
	}
	mark := "…"
	if !unicode {
		mark = "..."
	}
	budget := max - DisplayWidth(mark)
	if budget <= 0 {
		return ""
	}
	r := []rune(s)
	w, i := 0, len(r)
	for i > 0 {
		cw := RuneWidth(r[i-1])
		if w+cw > budget {
			break
		}
		w += cw
		i--
	}
	return mark + string(r[i:])
}
