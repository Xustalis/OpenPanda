package main

// The TUI's lipgloss theme. It derives every colour from the same brand facts
// the ANSI palette uses (cliui.Palette / #3DDC97), and honours the same
// NO_COLOR / TERM / unicode degradation rules — so `panda` looks like one
// product whether it renders through the classic status line or the full-screen
// Bubble Tea front end. When colour is disabled the styles collapse to identity
// (structure via borders and glyphs still reads on a monochrome console); when
// unicode is unavailable the rounded box degrades to an ASCII frame.

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// brandGreen is the OpenPanda wordmark colour, shared with cliui.Palette.Accent
// (61,220,151). Kept as a hex literal so lipgloss can downsample it to the
// terminal's real profile (24-bit → 256 → 16) instead of us guessing.
const brandGreen = lipgloss.Color("#3DDC97")

// theme is the resolved style set for one TUI run. Built once from the process
// palette (pal()) so a NO_COLOR / narrow-terminal decision is made in a single
// place, exactly like the ANSI side. It also carries the run's locale, so a
// block can label itself ("task", "thought") in the user's language without
// every render call threading a locale through.
type theme struct {
	color   bool
	unicode bool
	loc     i18n.Locale

	accent          lipgloss.Style // brand green, for the wordmark and active accents
	heading         lipgloss.Style // bold accent section titles
	muted           lipgloss.Style // dim secondary text (thoughts, hints, meta)
	italic          lipgloss.Style // reasoning preview
	success         lipgloss.Style
	warn            lipgloss.Style
	danger          lipgloss.Style
	command         lipgloss.Style // a typeable literal (slash command, flag)
	inputBox        lipgloss.Style // the bottom rounded input frame
	inputBoxRunning lipgloss.Style // the in-flight runtime input frame
	welcome         lipgloss.Style // the startup welcome frame
	approval        lipgloss.Style // the tier-2 approval card frame
}

// newTheme resolves the TUI styles from the shared palette. It reads pal() (the
// process-wide colour/unicode decision) rather than re-detecting, so the TUI
// and the status line never disagree about whether this terminal wants colour.
func newTheme(loc i18n.Locale) theme {
	p := pal()
	t := theme{color: p.Enabled(), unicode: p.Unicode(), loc: loc}

	// Base styles are identity until we know colour is wanted; that keeps the
	// NO_COLOR path free of stray escape sequences.
	t.accent = lipgloss.NewStyle()
	t.heading = lipgloss.NewStyle()
	t.muted = lipgloss.NewStyle()
	t.italic = lipgloss.NewStyle()
	t.success = lipgloss.NewStyle()
	t.warn = lipgloss.NewStyle()
	t.danger = lipgloss.NewStyle()
	t.command = lipgloss.NewStyle()

	if t.color {
		t.accent = t.accent.Foreground(brandGreen)
		t.heading = t.heading.Foreground(brandGreen).Bold(true)
		t.muted = t.muted.Faint(true)
		t.italic = t.italic.Faint(true).Italic(true)
		t.success = t.success.Foreground(lipgloss.Color("2"))
		t.warn = t.warn.Foreground(lipgloss.Color("3"))
		t.danger = t.danger.Foreground(lipgloss.Color("1"))
		t.command = t.command.Foreground(lipgloss.Color("6")).Bold(true)
	}

	// Frames: rounded on a unicode terminal, ASCII elsewhere so a bare Linux
	// console does not draw a wall of diamonds. The border tint follows colour.
	border := lipgloss.RoundedBorder()
	if !t.unicode {
		border = asciiBorder()
	}
	frame := lipgloss.NewStyle().Border(border).Padding(0, 1)
	t.inputBox = frame
	t.inputBoxRunning = frame
	t.welcome = frame
	t.approval = frame
	if t.color {
		t.inputBox = t.inputBox.BorderForeground(brandGreen)
		t.inputBoxRunning = t.inputBoxRunning.BorderForeground(lipgloss.Color("#E5C07B"))
		t.welcome = t.welcome.BorderForeground(brandGreen)
		t.approval = t.approval.BorderForeground(lipgloss.Color("3"))
	}
	return t
}

// asciiBorder is the degraded frame for terminals without box-drawing glyphs:
// pure ASCII, so it renders identically on a bare console and never turns into
// replacement characters.
func asciiBorder() lipgloss.Border {
	return lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	}
}

// lipglossNoStyle is the empty style, used to clear a widget's default styling
// (e.g. the textarea's cursor-line background) so our own frame shows through.
func lipglossNoStyle() lipgloss.Style { return lipgloss.NewStyle() }

// glyph picks a unicode symbol or its ASCII fallback, mirroring
// cliui.Palette.Glyph so the two front ends share one glyph vocabulary.
func (t theme) glyph(uni, ascii string) string {
	if t.unicode {
		return uni
	}
	return ascii
}

// stopButton renders a prominent red capsule button for stopping in-flight tasks.
func (t theme) stopButton() lipgloss.Style {
	st := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	if t.color {
		st = st.Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#D32F2F"))
	}
	return st
}

// steerButton renders a prominent brand green capsule button for injecting ideas.
func (t theme) steerButton() lipgloss.Style {
	st := lipgloss.NewStyle().Padding(0, 1).Bold(true)
	if t.color {
		st = st.Foreground(lipgloss.Color("#000000")).
			Background(brandGreen)
	}
	return st
}

// thoughtButton renders a subtle capsule button for toggling thought preview.
func (t theme) thoughtButton() lipgloss.Style {
	st := lipgloss.NewStyle().Padding(0, 1)
	if t.color {
		st = st.Foreground(lipgloss.Color("#E5C07B")).
			Background(lipgloss.Color("236"))
	}
	return st
}

// breathingColor returns a pulsing color that smoothly transitions between
// golden amber, neon cyan, and brand green to represent live activity.
func (t theme) breathingColor(tick int) lipgloss.TerminalColor {
	if !t.color {
		return lipgloss.NoColor{}
	}
	palette := []string{
		"#E5C07B",
		"#F39C12",
		"#00D7AF",
		"#3DDC97",
		"#2ECC71",
		"#3DDC97",
		"#00D7AF",
		"#F39C12",
	}
	idx := (tick / 2) % len(palette)
	return lipgloss.Color(palette[idx])
}
