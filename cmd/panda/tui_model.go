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
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// tuiMode is the model's top-level state.
type tuiMode int

const (
	modeSplash tuiMode = iota // Startup full-screen centered overlay
	modeIdle
	modeAsking
	modeApproving
	modeExec
	modeList        // Keyboard navigable list (/sessions, /projects, /resume)
	modeModelPanel  // Model management panel
	modeModelWizard // Model add/setup onboarding wizard
	modeOnboarding  // First-time use onboarding wizard
)

type listKind int

const (
	listNone listKind = iota
	listSessions
	listProjects
	listResume
)

type wizardStep int

const (
	wizardStepProvider  wizardStep = iota // Choose provider
	wizardStepAPIKey                      // Enter API Key
	wizardStepModelName                   // Enter Model Name
)

type onboardingStep int

const (
	onboardingStepLanguage    onboardingStep = iota // Choose UI language (default English)
	onboardingStepTerms                             // Terms of service & license agreement
	onboardingStepApproval                          // Command execution approval mode
	onboardingStepModelChoice                       // Configure model now or skip
	onboardingStepModelWizard                       // Interactive provider/key/model setup
)

// tuiModel is the Bubble Tea model. It borrows the live REPL (r) for its engine,
// conversation memory and slash-command handlers.
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

	// projName caches the active project for the status row.
	projName string

	mode         tuiMode
	stream       *askStream
	started      time.Time
	chatHistory  *chatHistory
	scrollOffset int

	// Navigation lists and panels
	listKind      listKind
	selectionList SelectionList

	// Onboarding state
	onboardingStep onboardingStep
	termsCursor    int // 0 = Agree [Y], 1 = Decline [N]

	// Model wizard state
	wizardStep     wizardStep
	wizardProvider string
	wizardKey      string
	wizardModel    string
	wizardInput    string

	// lastInterrupt timestamps the previous Esc/Ctrl-C of a turn.
	lastInterrupt time.Time

	// pendingPrompt is the user text of the in-flight ask, kept so the turn can
	// be recorded into conversation memory when it completes.
	pendingPrompt string

	// turnWorkDir is the worktree this turn runs in.
	turnWorkDir string

	// In-flight turn state.
	liveAnswer    string
	thought       []string
	thoughtDone   bool
	expandThought bool
	note          string

	// liveTask is the delegated-task card for this turn.
	liveTask *taskProgress

	// pending holds a task the engine parked for tier-2 approval.
	pending *askengine.Result

	// approvalSel is the focused choice on the approval card: 0 = approve, 1 = deny.
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

// chatHistory holds committed transcript blocks in memory for AltScreen rendering.
type chatHistory struct {
	blocks []block
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

	cHist := &chatHistory{}
	if r != nil && len(r.convo) > 0 {
		turns := r.convo
		const maxTurns = 10
		if len(turns)/2 > maxTurns {
			cHist.blocks = append(cHist.blocks, block{
				kind: blockNote,
				body: i18n.Tf(r.loc, "tui.replay.folded", "n", strconv.Itoa(len(turns)-maxTurns*2)),
			})
			turns = turns[len(turns)-maxTurns*2:]
		}
		for _, t := range turns {
			switch {
			case t.Role == "user":
				cHist.blocks = append(cHist.blocks, block{kind: blockUser, body: t.Content})
			case t.Role == "assistant" && strings.TrimSpace(t.Content) != "":
				cHist.blocks = append(cHist.blocks, block{kind: blockAnswer, body: t.Content})
			}
		}
	}

	return tuiModel{
		r:           r,
		th:          th,
		loc:         r.loc,
		engine:      r.engine,
		ta:          ta,
		sp:          sp,
		menu:        newSlashMenu(r.loc),
		mode:        modeSplash,
		started:     time.Now(),
		chatHistory: cHist,
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

// blockCommitMsg reports that a block was appended to chatHistory.
// It implements fmt.Stringer to format its rendered text, satisfying test assertions
// that inspect printed text from commands without writing directly to stdout in AltScreen.
type blockCommitMsg struct {
	text string
}

func (m blockCommitMsg) String() string {
	return m.text
}

// printBlock renders one committed transcript block into chatHistory and returns a
// command delivering blockCommitMsg. In full-screen AltScreen mode, committed blocks
// are rendered through mainChatView; tea.Println is avoided to prevent terminal scrolling.
func (m tuiModel) printBlock(b block) tea.Cmd {
	if m.chatHistory != nil {
		m.chatHistory.blocks = append(m.chatHistory.blocks, b)
	}
	rendered := "\n" + b.render(m.th, m.textWidth(), m.expandThought)
	return func() tea.Msg {
		return blockCommitMsg{text: rendered}
	}
}

// startupPrints is what the first WindowSizeMsg commits to scrollback: the
// welcome banner, then — when a previous run left a conversation behind — that
// conversation replayed as transcript blocks. A restored thread used to come
// back completely invisible: its turns existed only in the model's memory, so
// the screen showed a banner and an empty prompt no matter how many turns had
// been banked, and with the wheel belonging to the terminal there was nothing
// above the banner to scroll to either. The replay caps itself to the most
// recent turns — its job is orientation, not a verbatim wall — and says so when
// it folds. Each entry carries the same leading blank line printBlock uses, so
// the replay reads as part of the transcript's rhythm; the banner does not,
// because it is the frame the transcript hangs from.
func (m tuiModel) startupPrints() []string {
	prints := []string{m.welcome()}
	if m.r == nil || len(m.r.convo) == 0 {
		return prints
	}
	turns := m.r.convo
	const maxTurns = 10 // user+assistant pairs, taken from the tail
	add := func(b block) {
		prints = append(prints, "\n"+b.render(m.th, m.textWidth(), m.expandThought))
	}
	if n := len(turns) / 2; n > maxTurns {
		add(block{
			kind: blockNote,
			body: i18n.Tf(m.loc, "tui.replay.folded", "n", strconv.Itoa(len(turns)-maxTurns*2)),
		})
		turns = turns[len(turns)-maxTurns*2:]
	}
	for _, t := range turns {
		switch {
		case t.Role == "user":
			add(block{kind: blockUser, body: t.Content})
		case t.Role == "assistant" && strings.TrimSpace(t.Content) != "":
			add(block{kind: blockAnswer, body: t.Content})
		}
	}
	return prints
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
