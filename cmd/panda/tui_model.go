// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// The root Bubble Tea model for the full-screen interactive `panda` front end.
// It runs in the alternate screen, repainting the welcome banner, the committed
// transcript in chatHistory, the live turn and the input box each frame. The
// heavy lifting — classify/route/exec/judge, streaming, reasoning, approval —
// all lives behind the ask engine; this model is a thin, race-free front end
// that turns engine callbacks (delivered over a channel, see tui_msgs.go) into
// screen updates and keystrokes into asks.

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/skills"
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
	modeSkillsHub   // Skills Hub browse/search/install panel
	modeConfirm     // Yes/no card for a destructive command the exec pump cannot prompt for
)

type listKind int

const (
	listNone listKind = iota
	listSessions
	listProjects
	listResume
	listTasks
	listTeam
)

type wizardStep int

const (
	wizardStepProvider wizardStep = iota // Choose provider
	wizardStepForm                       // Single-screen field editor (tui_modelcfg.go)
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

	mode  tuiMode
	mouse mouseMode

	// x10Payload counts bytes of a split X10 mouse event that are still owed to
	// a message nobody will ever parse. X10 carries its coordinates as bare
	// bytes rather than decimal text, so those bytes arrive as ordinary
	// characters and would be typed into the prompt. See isX10MousePrelude in
	// tui_mouse.go.
	x10Payload int

	stream      *askStream
	exec        *commandExec
	execGen     uint64
	started     time.Time
	chatHistory *chatHistory

	// team mirrors the active conversation's roster for the status row; the
	// source of truth stays in the session (or the bare conversation's file)
	// and teamForAsk re-reads before an ask, so a roster changed in the web
	// console still binds the next turn.
	team []string
	// nextNode is the pending one-shot /node pin. It is cleared when the next
	// ask consumes it.
	nextNode string
	// teamMarks is the working set of the open roster picker: toggling changes
	// only this map until Enter applies it, so Esc is a true cancel.
	teamMarks map[string]bool
	// teamExtras is the picker's snapshot of roster entries the registry has
	// never heard of (a capability card's agent). They are drawn as rows of
	// their own so applying the picker cannot silently drop a name it also
	// could not show.
	teamExtras []string

	// pendingInputs is the busy-mode submission queue: lines the user enters
	// while a turn or an exec owns the foreground. They drain FIFO the moment
	// the foreground frees — a queued prompt becomes the next turn, a queued
	// command runs as the next exec. Nothing is ever reordered or dropped
	// silently; each enqueue is echoed into the transcript.
	//
	// A queued line is a WHOLE INPUT, not a fragment of the turn already
	// streaming. That is the contract the other harnesses keep (Claude Code,
	// Codex, Devin): typing while the agent works parks your next message, it
	// does not amend the one in flight. Folding it in made the model read the
	// same sentence twice and rewrote the persisted transcript to include
	// words the user had typed after sending.
	pendingInputs []string

	// scrollOffset is how many lines the transcript is scrolled back from the
	// live bottom; 0 means anchored at the bottom. It is clamped on BOTH ends:
	// the lower end here, and the upper end against scrollLimit.
	scrollOffset int
	// scrollLimit is the largest scrollOffset that still shows content — the
	// content height minus the viewport, republished by View on every render.
	//
	// It has to be shared through a pointer because View is a value method, as
	// the rest of the model is: it can read scrollOffset but cannot write it
	// back. Clamping only inside View (the obvious fix, and where maxScroll is
	// computed) would leave this field free to grow past the top, and every
	// scroll-down after an overshoot would then do nothing visible until the
	// phantom offset unwound. Holding a wheel for a moment is enough to build
	// one: 100 notches made the next ~62 notches of scrolling inert.
	//
	// It starts at MaxInt — "unbounded until the first render publishes a real
	// limit" — so a scroll that somehow arrives before the first frame is not
	// clamped against a limit of zero.
	scrollLimit *int

	// frameGeom records where the last frame actually placed its rows — how
	// many lines it emitted and which screen row the live region started on.
	// Click hitboxes (approval options, footer buttons) translate terminal
	// coordinates against this published geometry instead of re-deriving the
	// layout from m.height: the frame paints one row short of the terminal
	// (#44), and the live region floats to wherever the content left off, so
	// neither the row count nor the card's origin can be assumed. Shared
	// through a pointer for the same reason scrollLimit is — View is a value
	// method that can publish but cannot write back.
	frameGeom *tuiFrameGeom

	// activity caches the per-day task counts behind the welcome banner's
	// heatmap. The banner renders inside the value-receiver View on every
	// frame, so the store read lives behind a TTL'd pointer every model copy
	// shares — the same trick scrollLimit uses.
	activity *activityCache

	// Navigation lists and panels
	listKind      listKind
	selectionList SelectionList
	// taskListState is the state filter the open task picker was built with
	// ("" = all), so a watch event can rebuild the list in place.
	taskListState string
	// focusTask is the task the user last opened from the queue picker — the
	// default target for `/queue` and `/inject`, which otherwise have no way to
	// know which of a dozen rows the user means. Empty until a task is chosen;
	// the commands fall back to requiring an explicit id.
	focusTask string
	// streamTask is the task row the running ask created, reported by the
	// engine before it executes (StreamCallbacks.OnTaskRow). It is the target
	// a mid-turn Enter types into, and it outranks focusTask because it names
	// the work actually on screen. Cleared when the turn ends.
	streamTask string

	// Onboarding state
	onboardingStep onboardingStep
	termsCursor    int // 0 = Agree [Y], 1 = Decline [N]
	// termsScroll is the first clause row the terms card shows. The ten clauses
	// are far taller than a terminal, so the card windows them and this is where
	// the window sits; it resets whenever the step is (re-)entered.
	termsScroll int
	// termsReconsent marks the "license changed, re-accept" path: the terms
	// step shows alone (no language/approval wizard), and accepting goes
	// straight to idle instead of continuing the first-run wizard.
	termsReconsent bool

	// Model wizard/form state. The wizard* fields are the canonical values the
	// form edits (via mfieldGet/mfieldSet) and wizardConfig assembles; the
	// form* fields drive the editor's focus, cursor positions, picker overlay
	// and probe/fetch status.
	wizardStep      wizardStep
	wizardProvider  string
	wizardKey       string
	wizardModel     string
	wizardBaseURL   string // endpoint (prefilled from catalogue; editable)
	wizardAPIType   string // "openai" | "anthropic" (custom/relay)
	wizardThinking  string // "auto" | "on" | "off"
	wizardContext   string // context window, raw text
	wizardCtxManual bool   // user typed/cleared the context field — model changes stop re-suggesting it
	wizardMaxTok    string // completion cap, raw text — blank = catalogue auto-load
	wizardBudget    string // thinking budget, raw text
	wizardRetries   string // transport retries, raw text
	wizardParams    string // extra request params as a JSON object, raw text
	wizardHeaders   string // extra request headers as a JSON object, raw text
	wizardAlias     string // display name in the register
	wizardEditAlias string // alias being edited ("" = adding a new entry)

	form      []mfield // the single-screen editor's rows
	formFocus int      // focused row
	formErr   string   // validation error shown under the form
	// panelNotice carries a one-off failure from a panel action (deleting the
	// last model) into the panel title, so the error is visible instead of
	// being dropped by a discarded error return.
	panelNotice  string
	formTesting  bool   // connectivity probe in flight
	formTestErr  string // non-empty once the probe failed
	formFetching bool   // /models catalogue fetch in flight
	formFetchErr string // fetch failure shown under the form
	formPicking  bool   // fetched-models picker overlay is up

	// Panel state: the inline connectivity probe on the highlighted entry.
	panelTesting       bool
	panelTestName      string // alias the last probe ran against
	panelTestOK        bool
	panelTestErr       string
	panelTestDur       time.Duration
	confirmDeleteModel bool
	pendingDeleteModel string

	// lastInterrupt timestamps the previous Esc/Ctrl-C of a turn.
	lastInterrupt time.Time
	// turnMode is the slash-prefix mode this turn runs under ("goal", "plan",
	// "spec"; "" = normal). It drives the status badge and the classify
	// directive for the in-flight ask.
	turnMode string

	// Skills Hub panel state: the fetched catalogue, the "/" filter line, and
	// the async fetch/install status.
	hubIndex     *skills.HubIndex
	hubQuery     string
	hubSearching bool
	hubLoading   bool
	hubErr       string
	// hubInstalled caches the names already present in the global store. It is
	// refreshed when the panel opens, when an index lands, and after each
	// install — never inside hubRebuildList, which runs per search keystroke.
	hubInstalled map[string]bool

	// confirmText/confirmYes drive modeConfirm — the yes/no card that stands in
	// for the classic REPL's r.confirm, which reads a stdin the TUI owns.
	// confirmYes is the slash line the exec pump runs when the user confirms.
	confirmText string
	confirmYes  string

	// pendingPrompt is the user text of the in-flight ask, kept so the turn can
	// be recorded into conversation memory when it completes.
	pendingPrompt string

	// turnWorkDir is the worktree this turn runs in.
	turnWorkDir string

	// In-flight turn state. Chunks avoid repeatedly copying the complete answer
	// as streaming output grows; liveAnswer is materialized only for rendering.
	liveAnswerChunks []string
	liveAnswerBytes  int
	thought          []string
	thoughtDone      bool
	expandThought    bool
	// expandHints unfolds the troubleshooting tail on committed error blocks
	// (Ctrl+E). Like expandThought it is one flag for every block — AltScreen
	// repaints the transcript, so a toggle redraws what is already committed.
	expandHints bool
	note        string

	// liveRC memoizes the live answer's markdown render across frames — a
	// pointer every value-copy of the model shares, like scrollLimit and
	// activity. The update loop is single-goroutine, so no lock is needed.
	liveRC *liveRenderCache

	// welcomeC memoizes the welcome banner the same way.
	welcomeC *welcomeCache

	// liveTask is the delegated-task card for this turn.
	liveTask *taskProgress

	// pending holds a task the engine parked for tier-2 approval.
	pending *askengine.Result

	// pendingWorkDir is the tree the pending task resumes in once approved. A
	// card raised by this turn carries the turn's worktree; a card raised by
	// the watcher for out-of-band work leaves it empty, so the resume uses the
	// directory that task was submitted with instead of this session's.
	pendingWorkDir string

	// approvalSel is the focused choice on the approval card: 0 = approve, 1 = deny.
	approvalSel int

	// approvalScope is the card's selected remember scope (once|session|
	// project), preselected from the engine's resolved default when the card
	// opens and changeable per answer — the answer writes into that scope.
	approvalScope string

	animTick int
	quitting bool
}

// pulseStar is the thinking spinner: a star that swells and shrinks in place
// rather than a glyph that spins. It is the same ✻ the thought header and the
// welcome banner use, so "working" reads as one idea across the screen instead of
// three unrelated symbols, and the frames run up and back down so it breathes.
var pulseStar = spinner.Spinner{
	Frames: []string{"·", "✢", "✳", "∗", "✻", "✽", "✻", "∗", "✳", "✢"},
	FPS:    time.Second / 10,
}

// chatHistory holds committed transcript blocks in memory for AltScreen rendering.
type chatHistory struct {
	blocks []block

	// renders memoizes each block's rendered lines, aligned 1:1 with blocks.
	// Committed blocks are immutable, so an entry stays valid until a render
	// parameter (width, thought-fold, theme scalars) changes; any change drops
	// the whole memo rather than tracking keys per block. This is what keeps a
	// long transcript cheap: without it every frame re-renders — and
	// re-parses the markdown of — every block that ever committed.
	renders       [][]string
	renderW       int
	renderFold    bool
	renderHints   bool
	renderColor   bool
	renderUnicode bool
	renderLoc     i18n.Locale
	renderOK      bool
}

// clear empties the transcript. renders must go with blocks: the memo is
// index-aligned, so a bare blocks=nil would let the next appended block
// inherit the stale render of whichever entry held that slot before the wipe —
// the first turn after /clear would repaint as the previous session's first.
func (h *chatHistory) clear() {
	h.blocks = nil
	h.renders = nil
}

// renderedAt returns the cached rendered lines of blocks[i], filling entries
// lazily. The update loop is single-goroutine, so no lock is needed.
func (h *chatHistory) renderedAt(i int, t theme, width int, fold bool, hints bool) []string {
	if !h.renderOK || h.renderW != width || h.renderFold != fold || h.renderHints != hints ||
		h.renderColor != t.color || h.renderUnicode != t.unicode || h.renderLoc != t.loc {
		h.renders = h.renders[:0]
		h.renderW, h.renderFold, h.renderHints = width, fold, hints
		h.renderColor, h.renderUnicode, h.renderLoc = t.color, t.unicode, t.loc
		h.renderOK = true
	}
	if len(h.renders) > len(h.blocks) {
		h.renders = h.renders[:len(h.blocks)]
	}
	for len(h.renders) <= i {
		rendered := h.blocks[len(h.renders)].render(t, width, fold, hints)
		h.renders = append(h.renders, strings.Split(rendered, "\n"))
	}
	return h.renders[i]
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
	// "enter" in the default binding never fires — the outer handler submits on
	// Enter before the textarea sees it — so it is free to stay; ctrl+j is the
	// portable newline fallback alongside shift+enter / alt+enter.
	ta.KeyMap.InsertNewline.SetKeys("enter", "ctrl+m", "ctrl+j")
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
	if r.convoLen() > 0 {
		turns := r.convoNow()
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
		engine:      r.engine.Load(),
		ta:          ta,
		sp:          sp,
		menu:        newSlashMenu(r.loc),
		mode:        modeSplash,
		mouse:       resolveMouseMode(r.cfg),
		started:     time.Now(),
		chatHistory: cHist,
		team:        r.teamNow(),
		nextNode:    r.nextNodeNow(),
		scrollLimit: newScrollLimit(),
		frameGeom:   &tuiFrameGeom{liveTop: -1},
		activity:    &activityCache{},
		liveRC:      &liveRenderCache{},
		welcomeC:    &welcomeCache{},
	}
}

// newScrollLimit allocates the shared scroll ceiling. It begins unbounded: the
// first View replaces it with the real content height, and until then nothing
// should be clamped away (see tuiModel.scrollLimit).
func newScrollLimit() *int {
	limit := math.MaxInt
	return &limit
}

// Init focuses the input, starts the cursor blink, and arms the out-of-band
// task watcher. The welcome banner is part of the frame (see View), so it is
// not built here: Bubble Tea has not reported the terminal size yet, and an
// 80-column wordmark in a 52-column window would wrap.
func (m tuiModel) Init() tea.Cmd {
	if m.r != nil {
		m.r.resetWatchBaseline() // adopt already-finished tasks, so startup is quiet
	}
	return tea.Batch(textarea.Blink, watchTasks(m.r), altScrollCmd(m.mouse))
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
	rendered := "\n" + b.render(m.th, m.textWidth(), m.expandThought, m.expandHints)
	return func() tea.Msg {
		return blockCommitMsg{text: rendered}
	}
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
	if m.r == nil {
		return
	}
	loc := m.r.locale()
	if loc == m.loc {
		return
	}
	m.loc = loc
	m.th.loc = loc
	m.menu = newSlashMenu(loc)
	m.ta.Placeholder = i18n.T(loc, "tui.input.placeholder")
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
