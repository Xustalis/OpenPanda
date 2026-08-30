// Package cliui is OpenPanda's terminal presentation layer: one colour
// vocabulary, one spinner, one live status line, shared by every CLI surface.
//
// It exists because the colour handling used to be copy-pasted — five separate
// `"\x1b[" + code + "m" + s + "\x1b[0m"` sites, each with its own TTY check and
// none of them honouring NO_COLOR. A palette is built once from the terminal's
// actual capabilities and passed down; a disabled palette returns its input
// untouched, so callers never branch on "are we on a TTY" again.
//
// Nothing here knows about locales: every human-readable word arrives from the
// caller (already translated through internal/i18n).
package cliui

import (
	"os"
	"runtime"
	"strings"
)

// ANSI reset — every styled span closes with it, so a truncated line can never
// leak an attribute into the rest of the screen.
const reset = "\x1b[0m"

// Palette carries the terminal's presentation capabilities: whether colour may
// be emitted, whether 24-bit colour is available (the brand green degrades to
// plain green without it), and whether non-ASCII glyphs render at all — a bare
// Linux VT has the encoding but not the font.
//
// The zero Palette is the safe one: no colour, no unicode. That makes it the
// right default for pipes, CI logs and tests.
type Palette struct {
	color   bool
	rgb     bool
	unicode bool
}

// New builds a palette for a stream. tty says whether that stream is a
// terminal; unicode whether non-ASCII glyphs are expected to render. Both are
// facts the caller already knows (cmd/panda has stdoutIsTTY and
// termSupportsUnicode); the environment overrides below are ours to apply.
func New(tty, unicode bool) Palette {
	p := Palette{unicode: unicode}
	p.color = colorEnabled(tty)
	p.rgb = p.color && truecolor()
	return p
}

// Plain is the no-styling palette: every method returns its argument. Used for
// piped output, --json paths and tests.
func Plain() Palette { return Palette{} }

// colorEnabled resolves the colour question in the order the ecosystem has
// settled on: NO_COLOR wins over everything (no-color.org: any value, even
// "0"), then the explicit FORCE_COLOR/CLICOLOR_FORCE overrides, then TERM,
// then whether we are actually talking to a terminal.
func colorEnabled(tty bool) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	switch strings.ToLower(os.Getenv("FORCE_COLOR")) {
	case "":
		// not set — keep resolving
	case "0", "false", "none":
		return false
	default:
		return true
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if os.Getenv("CLICOLOR") == "0" {
		return false
	}
	switch os.Getenv("TERM") {
	case "dumb":
		// An explicit no-capability terminfo: never emit SGR.
		return false
	case "":
		// No terminfo to speak of. On unix that means emitting SGR would be
		// a guess, so colour stays off. Windows consoles (cmd.exe, PowerShell,
		// Windows Terminal) simply never set TERM, yet they speak ANSI once
		// the stream is a real terminal — so an empty TERM there enables
		// colour for TTY output instead of greyscaling every Windows session.
		return runtime.GOOS == "windows" && tty
	}
	return tty
}

// truecolor reports 24-bit colour support, which only COLORTERM advertises.
func truecolor() bool {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	return false
}

// Enabled reports whether this palette emits colour.
func (p Palette) Enabled() bool { return p.color }

// Unicode reports whether non-ASCII glyphs are expected to render.
func (p Palette) Unicode() bool { return p.unicode }

// Glyph picks between a unicode glyph and its ASCII stand-in.
func (p Palette) Glyph(uni, ascii string) string {
	if p.unicode {
		return uni
	}
	return ascii
}

// SGR wraps s in an arbitrary SGR parameter string ("1;33"). Prefer the named
// methods; this is the escape hatch for callers with a code already in hand.
func (p Palette) SGR(code, s string) string {
	if !p.color || s == "" || code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + reset
}

// The base vocabulary. Semantic names live below; these stay for the handful
// of places that genuinely mean "this word is yellow".
func (p Palette) Bold(s string) string    { return p.SGR("1", s) }
func (p Palette) Dim(s string) string     { return p.SGR("2", s) }
func (p Palette) Italic(s string) string  { return p.SGR("3", s) }
func (p Palette) Red(s string) string     { return p.SGR("31", s) }
func (p Palette) Green(s string) string   { return p.SGR("32", s) }
func (p Palette) Yellow(s string) string  { return p.SGR("33", s) }
func (p Palette) Blue(s string) string    { return p.SGR("34", s) }
func (p Palette) Magenta(s string) string { return p.SGR("35", s) }
func (p Palette) Cyan(s string) string    { return p.SGR("36", s) }

// Accent is the OpenPanda wordmark green — the one place a 24-bit terminal
// gets the exact brand colour and everything else gets plain green.
func (p Palette) Accent(s string) string {
	if p.rgb {
		return p.SGR("38;2;61;220;151", s)
	}
	return p.Green(s)
}

// Semantic layer. Call these from feature code so a later theme change is one
// edit here rather than a grep for "33".
func (p Palette) Success(s string) string { return p.Green(s) }
func (p Palette) Warn(s string) string    { return p.Yellow(s) }
func (p Palette) Danger(s string) string  { return p.Red(s) }
func (p Palette) Muted(s string) string   { return p.Dim(s) }
func (p Palette) Info(s string) string    { return p.Cyan(s) }

// Heading is a section title: bold, and accent-coloured when colour is on.
func (p Palette) Heading(s string) string {
	if p.rgb {
		return p.SGR("1;38;2;61;220;151", s)
	}
	return p.SGR("1;32", s)
}

// Command tints a literal the user can type back (a subcommand, a slash
// command, a flag) so scanning help output lands on the actionable words.
func (p Palette) Command(s string) string { return p.SGR("1;36", s) }
