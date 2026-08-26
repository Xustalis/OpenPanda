package cliui

// Spinner frame sets and the small glyph vocabulary the status line draws with.
//
// Two sets, chosen by the terminal's font rather than its encoding: braille
// dots on anything modern, and a four-frame ASCII wheel for the bare Linux VT
// (whose console font renders every non-ASCII rune as a diamond) and for
// terminals whose locale is not UTF-8.

import (
	"fmt"
	"strconv"
	"time"
)

// Braille is the default spinner: ten frames of U+280x dots, one glyph wide,
// which reads as smooth rotation at ~80ms per frame.
var Braille = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ASCIIWheel is the fallback spinner. Four frames, so it visibly ticks rather
// than pretending to be smooth.
var ASCIIWheel = []string{"-", "\\", "|", "/"}

// FrameInterval is one spinner step. Fast enough to read as motion, slow
// enough that a remote session over ssh is not repainting constantly.
const FrameInterval = 80 * time.Millisecond

// Frames picks the spinner set for a palette.
func Frames(p Palette) []string {
	if p.Unicode() {
		return Braille
	}
	return ASCIIWheel
}

// Mark glyphs for finished lines: success, failure, and the neutral bullet
// that prefixes progress notes.
func (p Palette) MarkOK() string     { return p.Glyph("✓", "+") }
func (p Palette) MarkFail() string   { return p.Glyph("✗", "x") }
func (p Palette) MarkBullet() string { return p.Glyph("·", "-") }
func (p Palette) MarkArrow() string  { return p.Glyph("→", "->") }

// Separator is the mid-line divider used between status fields and in the
// banner's hint list.
func (p Palette) Separator() string { return p.Glyph(" · ", " | ") }

// HumanDuration renders an elapsed time the way a progress line wants it:
// sub-second work in milliseconds, seconds with one decimal until it stops
// being interesting, then m/s. Never wider than 7 columns.
func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.Itoa(int(d.Milliseconds())) + "ms"
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	default:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm%02ds", m, int(d.Seconds())-m*60)
	}
}

// HumanCount abbreviates a token/byte count: 1234 → "1.2k", 1234567 → "1.2M".
// Exact below 1000, because "3 tokens" reads better than "0.0k".
func HumanCount(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return trimZero(float64(n)/1000) + "k"
	default:
		return trimZero(float64(n)/1_000_000) + "M"
	}
}

// trimZero renders one decimal place, dropping a trailing ".0" so counts read
// as "12k" rather than "12.0k".
func trimZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}
