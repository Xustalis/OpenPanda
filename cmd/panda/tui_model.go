package main

// The root Bubble Tea model for the full-screen interactive `panda` front end.
// It runs inline (committed turns flow into the terminal's own scrollback via
// tea.Println; only the in-flight region and the input box are repainted each
// frame), so quitting leaves the conversation on screen the way a shell session
// does. The heavy lifting — classify/route/exec/judge, streaming, reasoning,
// approval — all lives behind the ask engine; this model is a thin, race-free
// front end that turns engine callbacks (delivered over a channel, see
// tui_msgs.go) into screen updates and keystrokes into asks.

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// tuiMode is the model's top-level state. Keystrokes and incoming messages mean
// different things in each: idle waits for input, asking streams a reply,
// approving holds a tier-2 card until the user answers y/n, and exec has handed
// the terminal to a classic command handler.
type tuiMode int

const (
	modeIdle tuiMode = iota
	modeAsking
	modeApproving
	// modeExec is the window where a slash or "!" command owns the terminal, and
	// it exists to render nothing. tea.Exec releases the tty by stopping the
	// renderer, and stopping only erases the row the cursor sits on — everything
	// the last frame drew above that row stays behind. The idle frame is five rows,
	// so the blank line, the whole rounded box and the state row were stranded in
	// scrollback, the command's output printed under them, and a fresh box
	// repainted below that: two input bars for one command. An empty view first
	// lets the renderer's own flush clear the region (it erases below the cursor
	// whenever a frame shrinks), so the output lands where the box was.
	modeExec
)

// tuiModel is the Bubble Tea model. It borrows the live REPL (r) for its engine,
// conversation memory and — in later slices — slash-command handlers, so the TUI
// is a new front end over the same business logic rather than a fork of it.
type tuiModel struct {
	r      *repl
	th     theme
	loc    i18n.Locale
	engine *askengine.Engine

	ta textarea.Model
	sp spinner.Model

	// menu is the filterable slash-command popup, open while the input is a bare
	// "/token"; it lists the same replCommands the classic loop dispatches.
	menu slashMenu

	width  int
	height int
	ready  bool

	// projName caches the active project for the status row. The row is part of
	// the ephemeral frame, so reading the pointer at render time meant one SQLite
	// query per keystroke and per cursor blink; it is pushed instead — at startup
	// and after a foreground command (refreshProject), and off the Update loop by
	// the task watcher's poll, which is what catches a project entered elsewhere.
	projName string

	mode    tuiMode
	stream  *askStream
	started time.Time

	// lastInterrupt timestamps the previous Esc/Ctrl-C of a turn. A second one
	// inside interruptWindow quits outright, so an ask that cannot actually be
	// released — the core owns a delegated task's lifetime, not this front end
	// — can never trap the user in the program. It is the same double-tap the
	// classic loop uses, so both front ends feel alike.
	lastInterrupt time.Time

	// pendingPrompt is the user text of the in-flight ask, kept so the turn can
	// be recorded into conversation memory when it completes.
	pendingPrompt string

	// turnWorkDir is the worktree this turn runs in — the session's when a
	// session is bound, empty for a bare chat. A tier-2 task parked for
	// approval has to resume in the same tree, so it is kept until the turn
	// commits rather than re-derived at approval time.
	turnWorkDir string

	// In-flight turn state. liveAnswer accumulates streamed answer text (shown
	// in the ephemeral region, committed to scrollback when the turn ends);
	// thought holds chain-of-thought lines (display-only, D14); note is the
	// current lifecycle phase note (routing/running/…).
	liveAnswer    string
	thought       []string
	thoughtDone   bool
	expandThought bool
	note          string

	// liveTask is the delegated-task card for this turn, opened when the engine
	// reports it submitting a task and closed when the turn commits. nil for a
	// plain answer turn.
	liveTask *taskProgress

	// pending holds a task the engine parked for tier-2 approval; the model
	// shows its card and, on a yes, resumes it authorized.
	pending *askengine.Result

	// approvalSel is the focused choice on the approval card: 0 = approve,
	// 1 = deny. It starts on deny — the same safe default as the classic
	// [y/N] prompt — so arrows + Enter answer the card without reaching
	// for the y/n hotkeys (which keep working).
	approvalSel int

	animTick int
	quitting bool
}

// pulseStar is the thinking spinner: a star that swells and shrinks in place
// rather than a glyph that spins. It is the same ✻ the thought header and the
// welcome frame use, so "working" reads as one idea across the screen instead of
// three unrelated symbols, and the frames run up and back down so it breathes.
var pulseStar = spinner.Spinner{
	Frames: []string{"·", "✢", "✳", "∗", "✻", "✽", "✻", "∗", "✳", "✢"},
	FPS:    time.Second / 10,
}

// newTUIModel builds the model from a constructed repl (its engine, locale and
// stores are already wired by runRepl). The input starts focused; the spinner
// uses the unicode star pulse or an ASCII fallback so a bare console still
// animates.
func newTUIModel(r *repl) tuiModel {
	th := newTheme(r.loc)

	ta := textarea.New()
	ta.Placeholder = i18n.T(r.loc, "tui.input.placeholder")
	ta.Prompt = th.accent.Render(th.glyph("❯", ">")) + " "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 8
	ta.SetHeight(1)
	ta.Focus()
	// The bordered input frame supplies the visual box; the textarea itself must
	// not draw its own cursor-line background over it.
	ta.FocusedStyle.CursorLine = lipglossNoStyle()
	ta.BlurredStyle.CursorLine = lipglossNoStyle()

	sp := spinner.New()
	sp.Spinner = pulseStar
	if !th.unicode {
		sp.Spinner = spinner.Line
	}
	sp.Style = th.accent

	return tuiModel{
		r:      r,
		th:     th,
		loc:    r.loc,
		engine: r.engine,
		ta:     ta,
		sp:     sp,
		menu:   newSlashMenu(r.loc),
		mode:   modeIdle,
	}
}

// Init focuses the input, starts the cursor blink, prints the welcome banner
// into scrollback so it sits above the first prompt like a shell MOTD, and arms
// the out-of-band task watcher.
func (m tuiModel) Init() tea.Cmd {
	if m.r != nil {
		m.r.resetWatchBaseline() // adopt already-finished tasks, so startup is quiet
	}
	// The welcome frame is not printed here: Init runs before Bubble Tea reports
	// the terminal size, so a banner printed now would be drawn to the fallback
	// width — an 80-column box in a 52-column window. The first WindowSizeMsg
	// prints it (see Update), which is the earliest moment the frame can match
	// the terminal it is sitting in.
	return tea.Batch(textarea.Blink, watchTasks(m.r))
}

// printBlock renders one committed transcript block and pushes it into the
// terminal's scrollback. Every commit path goes through it so the transcript's
// content width is decided once: the same width the live region lays out to, so
// a streamed answer does not reflow the instant the turn commits.
func (m tuiModel) printBlock(b block) tea.Cmd {
	// The leading blank line is the transcript's spacing: one per block, so turns
	// separate into paragraphs instead of stacking into a wall of markers. It
	// belongs here rather than in render() because it is a property of committing
	// to scrollback — the live region draws the same blocks and supplies its own.
	return tea.Println("\n" + b.render(m.th, m.textWidth(), m.expandThought))
}

// refreshProject re-reads the active project pointer into the model; see the
// projName field for why the status row is pushed to rather than polled.
func (m *tuiModel) refreshProject() {
	if m.r == nil {
		m.projName = ""
		return
	}
	m.projName = m.r.activeProjectName()
}

// applyLocale re-reads the repl's locale into every front-end surface that
// captured it at startup: the theme's label locale, the slash menu's help
// lines (resolved once when the menu was built), and the input placeholder.
// /lang changes r.loc while this model holds its own snapshot, so without
// this pass the chrome — footer hints, menu help, detached notes — stays in
// the old language while only the handlers' printed output switches.
func (m *tuiModel) applyLocale() {
	if m.r == nil || m.r.loc == m.loc {
		return
	}
	m.loc = m.r.loc
	m.th.loc = m.r.loc
	m.menu = newSlashMenu(m.r.loc)
	m.ta.Placeholder = i18n.T(m.r.loc, "tui.input.placeholder")
}

// history assembles the conversation context for the next ask by delegating to
// the shared repl helper, so a session bound with /resume (which the TUI runs
// through the same command table) governs the TUI's turns too: the ask replays
// the thread and runs in its worktree. Bare mode replays this run's in-memory
// convo. The second result is the working directory, empty for bare mode.
func (m *tuiModel) history(prompt string) ([]entry.Turn, string) {
	if m.r == nil {
		return nil, ""
	}
	return m.r.askContext(prompt)
}
