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
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// tuiMode is the model's top-level state. Keystrokes and incoming messages mean
// different things in each: idle waits for input, asking streams a reply, and
// approving holds a tier-2 card until the user answers y/n.
type tuiMode int

const (
	modeIdle tuiMode = iota
	modeAsking
	modeApproving
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

	mode    tuiMode
	stream  *askStream
	started time.Time

	// pendingPrompt is the user text of the in-flight ask, kept so the turn can
	// be recorded into conversation memory when it completes.
	pendingPrompt string

	// In-flight turn state. liveAnswer accumulates streamed answer text (shown
	// in the ephemeral region, committed to scrollback when the turn ends);
	// thought holds chain-of-thought lines (display-only, D14); note is the
	// current lifecycle phase note (routing/running/…).
	liveAnswer    strings.Builder
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

	quitting bool
}

// newTUIModel builds the model from a constructed repl (its engine, locale and
// stores are already wired by runRepl). The input starts focused; the spinner
// uses a unicode braille cycle or an ASCII fallback so a bare console still
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
	sp.Spinner = spinner.MiniDot
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
	return tea.Batch(textarea.Blink, tea.Println(m.welcome()), watchTasks(m.r))
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
