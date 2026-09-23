//go:build !lite

package main

// Mouse ownership for the full-screen front end.
//
// The terminal can give the mouse to exactly one owner at a time, and it cannot
// be split: while cell-motion reporting is on, the terminal stops doing its own
// drag-select/double-click/⌘C and forwards every gesture to the app instead.
// That is why "wheel scrolling" and "select to copy" look like an either/or
// choice — see docs/confirmed-issues-fix-report.md §3.1.
//
// They are not an either/or. Every desktop terminal keeps an escape hatch for
// exactly this situation: a modifier held during the drag makes the terminal
// ignore the app's mouse reporting and run its own selection. iTerm2 uses
// Option — and copies the moment you let go, so no ⌘C is needed. Apple's
// Terminal.app uses Fn, and also offers ⌘R to toggle "Allow Mouse Reporting"
// outright. Capture therefore never costs copy; it costs copy-*without*-a-
// modifier, which is a much smaller thing to give up.
//
// Given that, the two modes differ only in which gesture needs the extra key:
//
//	mouseSelect (default) — drag/double-click copy outright; clicking a button
//	                        needs ctrl+t first.
//	mouseScroll            — wheel and clicks are native; selecting needs ⌥/Fn.
//
// mouseSelect is the default because the transcript is mostly text and copying
// it is the frequent action. Every click target also answers a keystroke (y/n,
// arrows+Enter, Esc), so nothing is *lost* in this mode — only a shortcut.
//
// The alt screen has no scrollback of its own, so simply dropping mouse capture
// would strand the transcript: the wheel would have nowhere to go. The fix is to
// hand the mouse back to the terminal *and* restore a scroll path that does not
// need the mouse:
//
//  1. alternate scroll (DECSET 1007) makes the terminal report wheel gestures as
//     Up/Down keys while the alt screen is active, so a trackpad swipe still
//     reaches us — as keystrokes rather than mouse events;
//  2. onArrowScroll turns those keys back into transcript scrolling when the
//     input box cannot use them anyway (single-line, no menu open);
//  3. PageUp/PageDown keep working in every mode on every terminal.
//
// So mouseSelect (the default) leaves copy working exactly as the terminal
// intends, and mouseScroll remains one keystroke away for users who would rather
// click the approval buttons than type y/n.

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// mouseMode records which party owns the terminal mouse.
type mouseMode int

const (
	// mouseSelect hands the mouse to the terminal: drag selects, double-click
	// picks a word, ⌘C (macOS) / Ctrl+Shift+C copies. Wheel gestures arrive as
	// arrow keys through alternate scroll.
	mouseSelect mouseMode = iota

	// mouseScroll keeps the mouse in the app: the wheel pages the transcript and
	// the selection lists, and clicks answer the approval and asking footers.
	// Terminal-native selection is unavailable until the mode is left.
	mouseScroll
)

// String is the token used by config, the environment override and tests. The
// config file spells the modes "select" and "scroll".
func (m mouseMode) String() string {
	if m == mouseScroll {
		return "scroll"
	}
	return "select"
}

// captured reports whether the app holds the mouse. It is the single place that
// decides between "we get MouseMsg" and "the terminal keeps the mouse".
func (m mouseMode) captured() bool { return m == mouseScroll }

// MouseEnvVar overrides ui.mouse for one run, the same way PANDA_CLASSIC_REPL
// overrides the front-end choice.
const MouseEnvVar = "PANDA_MOUSE"

// Alternate-scroll control sequences (DECSET/DECRST 1007). Inside the alternate
// screen the terminal then reports wheel gestures as Up/Down keys. We ask for it
// explicitly because a terminal is free to default it off; asking is harmless
// where it is already on, and harmless outside the alt screen where it is inert.
const (
	altScrollOn  = "\x1b[?1007h"
	altScrollOff = "\x1b[?1007l"
)

// parseMouseMode maps a config/env token to a mode. Only the two canonical
// spellings are accepted: an unrecognised value must fall through to the next
// source rather than silently picking a side.
func parseMouseMode(v string) (mouseMode, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "select":
		return mouseSelect, true
	case "scroll":
		return mouseScroll, true
	}
	return mouseSelect, false
}

// resolveMouseMode picks the startup mode: PANDA_MOUSE wins, then ui.mouse, then
// mouseSelect. The default leaves the mouse with the terminal so drag-select and
// ⌘C keep working, and the wheel still reaches the transcript as Up/Down through
// alternate scroll; ctrl+t swaps in mouseScroll for users who would rather click
// the approval buttons than type y/n.
func resolveMouseMode(cfg *config.Config) mouseMode {
	if m, ok := parseMouseMode(os.Getenv(MouseEnvVar)); ok {
		return m
	}
	if cfg != nil {
		if m, ok := parseMouseMode(cfg.UI.Mouse); ok {
			return m
		}
	}
	return mouseSelect
}

// mouseProgramCmd is the protocol switch that goes with a mode change: ask for
// cell-motion reporting (1002h/1006h) or hand the mouse back (1002l/1003l/1006l).
// It is one function rather than two call sites so a test can pin which side of
// the toggle each mode maps to.
func mouseProgramCmd(m mouseMode) tea.Cmd {
	if m.captured() {
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

// altScrollCmd moves the alternate-scroll flag along with the mode. Bubble Tea
// drives the mouse modes itself but knows nothing about DECSET 1007 (it never
// emits the sequence), so the flag has to follow ctrl+t by hand: a switch back
// to mouseSelect that skipped this would hand the mouse over and leave the wheel
// dead on every terminal that does not default 1007 on.
//
// Switching to mouseScroll releases the flag rather than leaving it set. xterm
// gives mouse reporting precedence, so the flag is inert there either way — but
// releasing it keeps the state we asked for equal to the state we rely on.
func altScrollCmd(m mouseMode) tea.Cmd {
	seq := altScrollOn
	if m.captured() {
		seq = altScrollOff
	}
	return func() tea.Msg {
		fmt.Fprint(os.Stdout, seq)
		return nil
	}
}

// toggleMouse flips who owns the mouse and reports it in the transcript. The
// returned command performs the switch: the mouse mode itself, then the
// alternate-scroll flag that has to shadow it. Sequence rather than Batch, so
// the flag is set after the mouse is released and read after the release lands.
// The note is what tells the user why a drag suddenly works (or stopped
// working) mid-session.
func (m tuiModel) toggleMouse() (tea.Model, tea.Cmd) {
	if m.mouse.captured() {
		m.mouse = mouseSelect
	} else {
		m.mouse = mouseScroll
	}
	note := block{kind: blockNote, body: i18n.T(m.loc, m.mouse.noteKey())}
	return m, tea.Sequence(
		mouseProgramCmd(m.mouse),
		altScrollCmd(m.mouse),
		m.printBlock(note),
	)
}

// noteKey is the transcript message announcing this mode. It always describes
// what is true *after* the switch, so the note and the hint line never disagree.
func (m mouseMode) noteKey() string {
	if m.captured() {
		return "tui.mouse.scrollOn"
	}
	return "tui.mouse.selectOn"
}

// isMouseToggleKey reports whether a keystroke asks for a mouse-mode switch.
// ctrl+t is free in the TUI (the textarea only binds it to the rarely used
// transpose-character edit, which the input box does not rely on), and F2 is the
// same action for terminals that swallow ctrl+t.
func isMouseToggleKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyCtrlT || msg.Type == tea.KeyF2
}

// x10PayloadLen is the payload of an X10 mouse event: Cb, Cx and Cy, one byte
// each. It is also the length of the "\x1b[M" prefix Bubble Tea reports in their
// place when a read boundary cuts the event in two.
const x10PayloadLen = 3

// Names of the two unexported Bubble Tea messages that carry raw input bytes.
// Neither can be named from outside the package, so they are matched on their
// type name.
const (
	unknownCSIMsgName       = "tea.unknownCSISequenceMsg"
	unknownInputByteMsgName = "tea.unknownInputByteMsg"
)

// isX10MousePrelude reports whether msg is the unrecognised CSI sequence Bubble
// Tea emits for a truncated X10 mouse event — the signal that the event's three
// coordinate bytes are about to arrive as ordinary characters.
//
// Only the type and the length are inspected; the bytes are left alone, and that
// is deliberate. Bubble Tea slices this message straight out of its 256-byte read
// buffer and refills that buffer as soon as the message is handed to the event
// loop, so by the time Update runs the contents are whatever the *next* read put
// there. A guard that read them would fire or not fire according to what the
// terminal happened to send next. (Measured: a burst long enough that the next
// read refills the whole buffer leaves `5b 2d 1b` in the prefix — a later event's
// coordinates, not the "\x1b[M" that started it. A shorter burst still reads
// intact, which makes it a coin flip rather than a rule.)
//
// Three bytes is the whole discriminator, and it is enough. The prefix of an X10
// event is exactly "\x1b[M", and Bubble Tea only reports it in this shape when it
// is all it managed to read: an event cut any earlier is held back and
// reassembled into a MouseMsg, and an intact event arrives as a MouseMsg anyway.
// An event cut exactly after the prefix is the one case that cannot be repaired,
// which is why it is the one case that needs this.
//
// Callers pair it with mouseMode.captured(), since a terminal only speaks X10
// while the app holds the mouse; that is what keeps a stray three-byte CSI from
// being swallowed.
func isX10MousePrelude(msg tea.Msg) bool {
	n, ok := unknownCSILen(msg)
	return ok && n == x10PayloadLen
}

// unknownCSILen reports the length of bubbletea's unknownCSISequenceMsg, a named
// []byte with no exported accessor. Only the slice header is touched, never the
// bytes — see isX10MousePrelude for why that distinction matters.
func unknownCSILen(msg tea.Msg) (int, bool) {
	if bubbleteaMsgName(msg) != unknownCSIMsgName {
		return 0, false
	}
	return reflect.ValueOf(msg).Len(), true
}

// bubbleteaMsgName reports the type name of msg, the only way to tell apart the
// unexported message kinds Bubble Tea builds out of raw input bytes.
func bubbleteaMsgName(msg tea.Msg) string {
	if t := reflect.TypeOf(msg); t != nil {
		return t.String()
	}
	return ""
}

// x10PayloadBytes reports how many raw input bytes msg carries, for the messages
// Bubble Tea builds out of plain bytes. ok is false for everything else, which
// means an X10 event was not actually split and no bytes are outstanding.
//
// Three shapes turn up inside an X10 payload: ordinary characters, several of
// which share one KeyRunes message; a space, which is button 0's Cb; and a byte
// that is not valid UTF-8 on its own, which is any coordinate past column 95.
func x10PayloadBytes(msg tea.Msg) (int, bool) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyRunes, tea.KeySpace:
			return len(k.Runes), true
		}
		return 0, false
	}
	if bubbleteaMsgName(msg) == unknownInputByteMsgName {
		return 1, true
	}
	return 0, false
}
