//go:build !lite

package main

// Update: the model's event loop. It routes keystrokes by mode and folds engine
// events (delta/reasoning/progress/done) into the in-flight turn. Committed
// output is pushed to scrollback with tea.Println; the ephemeral View holds only
// the live region and the input box.

import (
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

var (
	// SGR mouse full or partial sequence: "<35;20;10M", "[<35;20;10M", "35;20;10M", "20;10M"
	sgrMouseRe = regexp.MustCompile(`^(?:\[?<)?\d+;\d+(?:;\d+)?[Mm]$`)
	// Severed SGR tail starting with semicolon: ";20M", ";123;20M", ";20", ";123;20;"
	sgrMouseTailRe = regexp.MustCompile(`^;\d+(?:;\d+)*(?:[Mm]|;)?$`)
	// Severed SGR head starting with `<` or `[<`: "<35;10", "<35;", "[<35;10", "[<35;", "[<"
	sgrMouseHeadRe = regexp.MustCompile(`^(?:\[?<|<)\d*(?:;\d*)*[Mm]?$`)
	// Concatenated SGR mouse stream: e.g. "<35;1;2M<35;1;3M" or "[<35;1;2M[<35;1;3M"
	sgrMouseStreamRe = regexp.MustCompile(`^(?:\[?<\d+;\d+;\d+[Mm])+$`)

	// Cursor Position Report (CPR): "24;80R", "[24;80R", ";80R"
	cprReportRe = regexp.MustCompile(`^(?:\[\d+|\d+|);\d+R$`)

	// Private mode reports (DECSET/DECRST/DA): "[?1002h", "?1002h", "[?1;2c", "?1;2c", "?1007h"
	privateModeRe = regexp.MustCompile(`^\[?\?\d+(?:;\d+)*[a-zA-Z]$`)

	// Function key / keypad / bracketed paste / tilde residues:
	// "[1~", "1~", "[3~", "3~", "[200~", "200~", "[201~", "201~"
	tildeKeyRe = regexp.MustCompile(`^\[?\d+(?:;\d+)*~$`)
	// Modifier cursor keys: "[1;2A", "1;2A", "[1;5C", "1;5D"
	csiModifierRe = regexp.MustCompile(`^\[?1;\d+[A-Za-z]$`)
	// Window size report: "[8;24;80t", "8;24;80t"
	winSizeReportRe = regexp.MustCompile(`^\[?8;\d+;\d+t$`)
	// Kitty keyboard protocol: "[97u", "97;1u"
	kittyKeyRe = regexp.MustCompile(`^\[?\d+(?:;\d+)*u$`)
	// A modified Enter under the kitty keyboard protocol: CSI 13;mod u, where
	// mod > 1 carries shift(1)/alt(2)/ctrl(4) — "[13;2u" is Shift+Enter. We
	// push the disambiguate flag at startup (see runTUI); terminals that
	// answer send these instead of a bare CR, and any modifier on Enter means
	// "newline, not submit" by the same convention editors use.
	kittyEnterRe = regexp.MustCompile(`^\[?13;\d+u$`)
	// SS3 function keys: "[OP]", "OP", "OQ", "OR", "OS"
	ss3KeyRe = regexp.MustCompile(`^(?:\[O|O)[P-S]$`)
	// OSC responses: "]11;rgb:...", "]10;..."
	oscResponseRe = regexp.MustCompile(`^\]?\d+;.*$`)
)

// isLeakedEscapeFragment reports whether a KeyMsg is an unparsed or split terminal escape
// sequence (e.g. SGR mouse tracking events like "[<65;123;20M" or residues like ";20M",
// cursor position reports like "24;80R", focus reports like "[I", or mode control markers)
// that should be dropped rather than appended into the user input prompt.
func isLeakedEscapeFragment(msg tea.KeyMsg) bool {
	// 1. Check for Alt-prefixed escape starters.
	// When Bubble Tea encounters an unparsed ESC sequence starting with \x1b[, \x1bO, \x1b],
	// it parses \x1b as Alt: true and emits the next byte as a single rune with Alt: true.
	if msg.Alt && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case '[', 'O', ']', '?', '<', ';', '~', '\x1b':
			return true
		}
	}

	if msg.Type != tea.KeyRunes {
		return false
	}
	s := string(msg.Runes)
	if len(s) == 0 {
		return false
	}

	// Raw escape character embedded
	if strings.ContainsRune(s, '\x1b') {
		return true
	}

	// Focus reports: "[I", "[O"
	if s == "[I" || s == "[O" {
		return true
	}

	// Any fragment containing "[<" (SGR mouse prefix)
	if strings.Contains(s, "[<") {
		return true
	}

	// SGR mouse full, severed, or stream sequences
	if sgrMouseRe.MatchString(s) || sgrMouseTailRe.MatchString(s) || sgrMouseHeadRe.MatchString(s) || sgrMouseStreamRe.MatchString(s) {
		return true
	}

	// Cursor Position Reports
	if cprReportRe.MatchString(s) {
		return true
	}

	// Private mode reports (e.g. "[?1002h", "?1007h", "?1;2c")
	if strings.HasPrefix(s, "[?") || privateModeRe.MatchString(s) {
		return true
	}

	// Bracketed paste & tilde function keys
	if tildeKeyRe.MatchString(s) {
		return true
	}

	// CSI modifier keys
	if csiModifierRe.MatchString(s) {
		return true
	}

	// Window reports, Kitty keys, SS3 keys, OSC responses
	if winSizeReportRe.MatchString(s) || kittyKeyRe.MatchString(s) || ss3KeyRe.MatchString(s) || oscResponseRe.MatchString(s) {
		return true
	}

	return false
}

// isModifiedEnter reports whether the keystroke is an Enter carrying a
// modifier — the universal "newline, not submit" gesture:
//   - kitty CSI-u: "[13;2u" (shift), "[13;4u" (ctrl), "[13;6u" (shift+ctrl)…
//     — delivered as KeyRunes because bubbletea v1 does not parse CSI-u keys
//   - ESC CR: Alt+Enter, parsed as KeyEnter{Alt:true}
//   - Ctrl+J (LF): the classic fallback, works on every terminal
//
// Plain Enter keeps its "submit" meaning everywhere.
func isModifiedEnter(msg tea.KeyMsg) bool {
	if msg.Type == tea.KeyEnter && msg.Alt {
		return true
	}
	if msg.Type == tea.KeyCtrlJ {
		return true
	}
	return msg.Type == tea.KeyRunes && kittyEnterRe.MatchString(string(msg.Runes))
}

// insertNewline feeds a line break into the prompt textarea and grows the box
// to fit, honouring the 8-row cap used everywhere else the height is set.
func (m *tuiModel) insertNewline() {
	m.ta.InsertString("\n")
	m.ta.SetHeight(min(8, max(1, m.ta.LineCount())))
	m.menu.sync(m.ta.Value(), m.argResolve())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A terminal without SGR mouse support answers in X10, whose coordinates
	// travel as bare bytes rather than decimal text. Bubble Tea reads into a
	// fixed 256-byte buffer and treats a short read as an event boundary, so a
	// burst of wheel notches — a trackpad flick is dozens — can end mid-event
	// and strand those bytes in the prompt as literal text. One position comes
	// out as `[- and another as 13M; by shape they are indistinguishable from
	// typing, which is why isLeakedEscapeFragment cannot catch them.
	//
	// The prefix does surface, as an unrecognised CSI, and it is the only handle
	// we get on the event. It says how many bytes the event still owes; swallow
	// exactly those and the coordinates never reach the prompt.
	if m.x10Payload > 0 {
		n, ok := x10PayloadBytes(msg)
		switch {
		case !ok:
			// The event was not split after all, or the stream has moved on.
			// Drop the expectation rather than eat a later keystroke.
			m.x10Payload = 0
		case n <= m.x10Payload:
			m.x10Payload -= n
			return m, nil
		default:
			// More bytes than the event owed: the rest is real typing. Only a
			// KeyRunes message can carry more than one byte, so this is the
			// only shape that gets here.
			k := msg.(tea.KeyMsg)
			k.Runes = k.Runes[m.x10Payload:]
			m.x10Payload = 0
			msg = k
		}
	}
	if m.mouse.captured() && isX10MousePrelude(msg) {
		m.x10Payload = x10PayloadLen
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		first := !m.ready
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		// Clamp to the actual available width; a tiny terminal must shed
		// decoration rather than creating a component wider than the screen.
		m.ta.SetWidth(max(1, msg.Width-4))
		if first {
			m.refreshProject()
			if m.mode != modeSplash {
				prints := m.startupPrints()
				cmds := make([]tea.Cmd, len(prints))
				for i, p := range prints {
					text := p
					cmds[i] = func() tea.Msg { return blockCommitMsg{text: text} }
				}
				return m, tea.Batch(cmds...)
			}
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		if isModifiedEnter(msg) {
			switch m.mode {
			case modeIdle, modeAsking:
				m.insertNewline()
				return m, nil
			}
			// Everywhere else — pickers, wizard fields, confirmations — a
			// newline is meaningless. A modifier on the Enter key itself
			// (Alt+Enter, kitty CSI-u) degrades to plain Enter; Ctrl+J is not
			// an Enter at all and stays inert outside the text modes (vim
			// muscle memory expects it to move the cursor, not to confirm).
			if msg.Type == tea.KeyCtrlJ {
				return m, nil
			}
			return m.onKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
		if isLeakedEscapeFragment(msg) {
			return m, nil
		}
		return m.onKey(msg)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case spinner.TickMsg:
		m.animTick++
		// Outside a turn the spinner still has work to do while a model
		// probe or catalogue fetch is in flight in the config surfaces.
		if m.mode != modeAsking && !m.formTesting && !m.formFetching && !m.panelTesting {
			return m, nil
		}
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case deltaMsg:
		if msg.stream == nil || msg.stream != m.stream || msg.stream.detached {
			return m, nil
		}
		return m.onDelta(msg)
	case reasoningMsg:
		if msg.stream == nil || msg.stream != m.stream || msg.stream.detached {
			return m, nil
		}
		m.thought = appendReasoning(m.thought, msg.text)
		return m, waitForActivity(msg.stream)
	case progressMsg:
		if msg.stream == nil || msg.stream != m.stream || msg.stream.detached {
			return m, nil
		}
		return m.onProgress(msg)
	case doneMsg:
		return m.onDone(msg)
	case resumedMsg:
		return m.onResumed(msg)
	case watchMsg:
		return m.onWatch(msg)
	case execOutputMsg:
		if msg.exec == nil || msg.exec != m.exec || msg.generation != m.execGen {
			return m, nil
		}
		m.execText.WriteString(msg.text)
		return m, waitForExec(msg.exec)
	case execDoneMsg:
		if msg.exec == nil || msg.exec != m.exec || msg.generation != m.execGen {
			return m, nil
		}
		// A slash/shell command finished; commit its user echo and captured output
		// to chatHistory and return to modeIdle.
		m.mode = modeIdle
		m.exec = nil
		m.execText.Reset()
		m.applyLocale()
		m.refreshProject()
		if m.chatHistory != nil {
			if msg.text == "/new" {
				m.chatHistory.blocks = nil
			} else if msg.text != "" {
				m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: msg.text})
			}
			if strings.TrimSpace(msg.output) != "" {
				m.chatHistory.blocks = append(m.chatHistory.blocks, block{
					kind: blockInfo,
					body: strings.TrimRight(msg.output, "\n"),
				})
			}
		}
		m.scrollOffset = 0
		return m, nil
	case blockCommitMsg:
		return m, nil
	case droppedMsg:
		return m, nil
	case wizardTestMsg:
		if m.mode != modeModelWizard && !(m.mode == modeOnboarding && m.onboardingStep == onboardingStepModelWizard) {
			return m, nil // the user navigated away mid-probe
		}
		m.formTesting = false
		if msg.err == nil {
			// Connectivity proven — persist and activate immediately.
			return m.finalizeWizard()
		}
		m.formTestErr = msg.err.Error()
		return m, nil
	case modelListMsg:
		if m.mode != modeModelWizard && !(m.mode == modeOnboarding && m.onboardingStep == onboardingStepModelWizard) {
			return m, nil // stale fetch — the user moved on
		}
		m.formFetching = false
		if msg.err != nil {
			m.formFetchErr = msg.err.Error()
			return m, nil
		}
		if len(msg.models) == 0 {
			m.formFetchErr = i18n.T(m.loc, "tui.mform.fetchEmpty")
			return m, nil
		}
		items := make([]SelectionItem, len(msg.models))
		for i, id := range msg.models {
			items[i] = SelectionItem{Index: i + 1, ID: id, Title: id}
		}
		sl := NewSelectionList(i18n.T(m.loc, "tui.mform.pickTitle"), items)
		sl.Boxed = true
		sl.FooterHints = i18n.T(m.loc, "tui.wizard.confirmBack")
		// Pre-highlight the current model value when it appears in the list.
		for i, it := range items {
			if it.ID == m.wizardModel {
				sl.Cursor = i
				break
			}
		}
		m.selectionList = sl
		m.formPicking = true
		return m, nil
	case panelTestMsg:
		m.panelTesting = false
		m.panelTestName = msg.alias
		m.panelTestDur = msg.dur
		m.panelTestOK = msg.err == nil
		if msg.err != nil {
			m.panelTestErr = msg.err.Error()
		} else {
			m.panelTestErr = ""
		}
		return m, nil
	case skillsHubIndexMsg:
		if m.mode != modeSkillsHub {
			return m, nil // stale fetch — user left the panel
		}
		return m.onSkillsHubIndex(msg)
	case skillsHubInstallMsg:
		// The install toast commits even if the user navigated away — the
		// skill landed (or failed) either way.
		return m.onSkillsHubInstall(msg)
	}
	return m, nil
}

// wizardTestMsg carries the model form's connectivity probe result back to
// the Update loop (nil error = the endpoint answered).
type wizardTestMsg struct {
	err error
}

// modelListMsg carries a fetched /models catalogue back to the form's picker.
type modelListMsg struct {
	models []string
	err    error
}

// panelTestMsg is the result of the model panel's inline connectivity probe:
// which alias was probed, the outcome, and how long it took.
type panelTestMsg struct {
	alias string
	err   error
	dur   time.Duration
}

// interruptWindow is how long a second Esc/Ctrl-C during a turn counts as
// "quit" rather than a second cancel.
const interruptWindow = time.Second

// transcriptScrollStep is how far one wheel notch — or the arrow key a wheel
// notch becomes under alternate scroll — moves the transcript. Both paths share
// it so scrolling feels identical whether or not the app owns the mouse.
const transcriptScrollStep = 3

// scrollTranscript moves the transcript viewport: positive reads back toward
// older output, negative returns toward the live bottom. Offset 0 is the bottom
// anchor; the far end is clamped here against the content height View last
// published, so an overshoot cannot leave a phantom offset behind that makes the
// next few scroll-downs do nothing (see tuiModel.scrollLimit).
func (m *tuiModel) scrollTranscript(lines int) {
	offset := max(0, m.scrollOffset+lines)
	if m.scrollLimit != nil {
		offset = min(offset, max(0, *m.scrollLimit))
	}
	m.scrollOffset = offset
}

// pageLines is the jump PageUp/PageDown make: half a screen, at least one line,
// with a sane floor for a terminal too short to do arithmetic on.
func (m tuiModel) pageLines() int {
	avail := m.height - 4
	if avail <= 0 {
		avail = 10
	}
	return max(1, avail/2)
}

// arrowScrollLines maps an arrow key to the transcript movement it performs.
// When scrolled up into history (scrollOffset > 0), Up/Down arrows always navigate history.
// When at bottom, select mode maps Up/Down to history scroll.
func (m tuiModel) arrowScrollLines(msg tea.KeyMsg) (int, bool) {
	if m.scrollOffset > 0 {
		switch msg.Type {
		case tea.KeyUp:
			return transcriptScrollStep, true
		case tea.KeyDown:
			return -transcriptScrollStep, true
		}
	}
	if m.mouse.captured() {
		return 0, false
	}
	switch msg.Type {
	case tea.KeyUp:
		return transcriptScrollStep, true
	case tea.KeyDown:
		return -transcriptScrollStep, true
	}
	return 0, false
}

// arrowsScrollHere reports whether the transcript should take Up/Down in the
// chat modes. When scrolled up into history (scrollOffset > 0), arrows always navigate
// history. When at bottom, a single-line draft in select mode gives Up/Down to transcript.
func (m tuiModel) arrowsScrollHere() bool {
	if m.scrollOffset > 0 {
		return true
	}
	if m.mouse.captured() {
		return false
	}
	return m.ta.LineCount() <= 1
}

// onKey dispatches a keystroke according to the current mode.
func (m tuiModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+t/F2 switch who owns the mouse. That is a device toggle rather than an
	// editing key, so it answers in every mode — including mid-turn and while a
	// slash command is running, which is exactly when a user notices that a drag
	// cannot select anything.
	if isMouseToggleKey(msg) {
		return m.toggleMouse()
	}
	if m.mode == modeExec {
		if lines, ok := m.arrowScrollLines(msg); ok { // the input box is frozen here
			m.scrollTranscript(lines)
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			if m.exec != nil {
				m.exec.cancel()
			}
			return m, nil
		case tea.KeyPgUp:
			m.scrollTranscript(m.pageLines())
			return m, nil
		case tea.KeyPgDown:
			m.scrollTranscript(-m.pageLines())
			return m, nil
		case tea.KeyHome:
			if m.scrollLimit != nil {
				m.scrollTranscript(*m.scrollLimit)
			}
			return m, nil
		case tea.KeyEnd:
			m.scrollOffset = 0
			return m, nil
		default:
			return m, nil
		}
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.mode == modeAsking {
			return m.interrupt()
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyCtrlO:
		m.expandThought = !m.expandThought
		return m, nil
	}

	switch m.mode {
	case modeSplash:
		return m.onSplashKey(msg)
	case modeOnboarding:
		return m.handleOnboardingKey(msg)
	case modeList:
		return m.handleListKey(msg)
	case modeModelPanel:
		return m.handleModelPanelKey(msg)
	case modeModelWizard:
		return m.handleModelWizardKey(msg)
	case modeSkillsHub:
		return m.handleSkillsHubKey(msg)
	case modeAsking:
		if m.arrowsScrollHere() {
			if lines, ok := m.arrowScrollLines(msg); ok {
				m.scrollTranscript(lines)
				return m, nil
			}
		}
		if msg.Type == tea.KeyPgUp {
			m.scrollTranscript(m.pageLines())
			return m, nil
		}
		if msg.Type == tea.KeyPgDown {
			m.scrollTranscript(-m.pageLines())
			return m, nil
		}
		if msg.Type == tea.KeyHome {
			if m.scrollLimit != nil {
				m.scrollTranscript(*m.scrollLimit)
			}
			return m, nil
		}
		if msg.Type == tea.KeyEnd {
			m.scrollOffset = 0
			return m, nil
		}
		if msg.Type == tea.KeyEsc {
			if strings.TrimSpace(m.ta.Value()) != "" {
				m.ta.Reset()
				m.ta.SetHeight(1)
				return m, nil
			}
			return m.interrupt()
		}
		if msg.Type == tea.KeyEnter {
			text := strings.TrimSpace(m.ta.Value())
			if text == "" {
				return m, nil
			}
			if m.stream == nil || !m.stream.injectSteer(text) {
				note := block{kind: blockError, body: i18n.T(m.loc, "tui.steer.full")}
				return m, m.printBlock(note)
			}
			m.ta.Reset()
			m.ta.SetHeight(1)
			steerPrefix := i18n.T(m.loc, "tui.turn.steerPrefix")
			m.pendingPrompt += "\n" + steerPrefix + text
			if m.r != nil {
				m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: steerPrefix + text})
			}
			blk := block{
				kind: blockNote,
				body: m.th.accent.Render("💡 ") + i18n.Tf(m.loc, "tui.turn.steered", "idea", text),
			}
			return m, m.printBlock(blk)
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		m.ta.SetHeight(min(8, max(1, m.ta.LineCount())))
		return m, cmd
	case modeApproving:
		return m.onApprovalKey(msg)
	default:
		return m.onIdleKey(msg)
	}
}

// onSplashKey handles keys on the splash screen overlay.
func (m tuiModel) onSplashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "q", "Q":
			m.quitting = true
			return m, tea.Quit
		}
		newM, cmd := m.startFromSplash()
		if tm, ok := newM.(tuiModel); ok && tm.mode == modeIdle {
			var taCmd tea.Cmd
			tm.ta, taCmd = tm.ta.Update(msg)
			return tm, tea.Batch(cmd, taCmd)
		}
		return newM, cmd
	default:
		return m.startFromSplash()
	}
}

// startFromSplash transitions from splash to the model onboarding guide or main idle chat.
func (m tuiModel) startFromSplash() (tea.Model, tea.Cmd) {
	if m.r != nil && m.r.cfg != nil && !m.r.cfg.UI.Onboarded {
		return m.startOnboarding()
	}

	hasModel := false
	if m.r != nil && m.r.cfg != nil {
		if m.r.cfg.Model.BaseURL != "" || m.r.cfg.Model.Provider != "" || len(m.r.cfg.Models) > 0 {
			hasModel = true
		}
	}

	if !hasModel {
		return m.startModelWizard()
	}

	m.mode = modeIdle
	m.scrollOffset = 0
	return m, nil
}

// interrupt answers Esc/Ctrl-C during a turn. It releases this front end from
// the ask and signals cancellation.
//
// Pressing twice inside interruptWindow quits. That is the escape hatch for the
// case where nothing can be released at all, and it is what keeps a wedged
// turn from trapping the user in the program.
func (m tuiModel) interrupt() (tea.Model, tea.Cmd) {
	now := time.Now()
	if !m.lastInterrupt.IsZero() && now.Sub(m.lastInterrupt) < interruptWindow {
		m.quitting = true
		return m, tea.Quit
	}
	m.lastInterrupt = now

	if m.stream == nil {
		note := block{kind: blockNote, body: i18n.T(m.loc, "tui.turn.busy")}
		return m, m.printBlock(note)
	}

	m.stream.drop()
	m.mode = modeIdle
	m.stream = nil
	m.resetLive()
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder")
	note := block{kind: blockNote, body: m.th.warn.Render("⏹ ") + i18n.T(m.loc, "tui.turn.stopped")}
	return m, tea.Batch(
		m.turnEnded(),
		m.printBlock(note),
	)
}

// onIdleKey handles input while waiting at the prompt. When the slash-command
// menu is open it steals the navigation keys (arrows/Tab/Enter/Esc); otherwise
// Enter submits, Esc clears the line, Ctrl+D on an empty line quits, and every
// other key edits the textarea (after which the menu re-syncs to the new text).
func (m tuiModel) onIdleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.menu.active {
		if handled, next, cmd := m.onMenuKey(msg); handled {
			return next, cmd
		}
	}
	// While the terminal owns the mouse, alternate scroll turns a wheel notch
	// into Up/Down, so the transcript takes them whenever the input box cannot.
	if m.arrowsScrollHere() {
		if lines, ok := m.arrowScrollLines(msg); ok {
			m.scrollTranscript(lines)
			return m, nil
		}
	}
	switch msg.Type {
	case tea.KeyPgUp:
		m.scrollTranscript(m.pageLines())
		return m, nil
	case tea.KeyPgDown:
		m.scrollTranscript(-m.pageLines())
		return m, nil
	case tea.KeyHome:
		if m.scrollLimit != nil {
			m.scrollTranscript(*m.scrollLimit)
		}
		return m, nil
	case tea.KeyEnd:
		m.scrollOffset = 0
		return m, nil
	case tea.KeyEnter:
		m.scrollOffset = 0
		text := strings.TrimSpace(m.ta.Value())
		if text == "" {
			return m, nil
		}
		return m.submit(text)
	case tea.KeyEsc:
		if m.scrollOffset > 0 {
			m.scrollOffset = 0
			return m, nil
		}
		m.ta.Reset()
		m.menu.close()
		return m, nil
	case tea.KeyCtrlD:
		if strings.TrimSpace(m.ta.Value()) == "" {
			m.quitting = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	// Grow the input box with its content up to the cap, so multi-line prompts
	// (Ctrl+J inserts a newline) stay fully visible.
	m.ta.SetHeight(min(8, max(1, m.ta.LineCount())))
	// Re-filter the popup against the edited line: it opens on a bare "/token",
	// then follows the argument position — "/lang " lists locale codes, the
	// token under the cursor filters them — and closes on a non-slash line.
	m.menu.sync(m.ta.Value(), m.argResolve())
	return m, cmd
}

// onMouse handles terminal mouse events across all modes. It only fires while
// the app owns the mouse (mouseScroll — see tui_mouse.go); in the default
// mouseSelect the terminal keeps the mouse, so a wheel notch reaches us as an
// arrow key instead and is handled by arrowScrollLines. Wheel gestures here
// scroll the transcript in the chat modes and page the selection in the list
// modes, where scrollOffset has no meaning. Clicks reach the approval options,
// the asking-mode footer buttons, and the slash-menu rows; every one of those
// surfaces also answers its keyboard path (y/n, Esc/Enter, arrows+Tab).
func (m tuiModel) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}

	// The wheel is handled before the mode gate below, so it stays live even
	// mid-exec. Reading back is exactly what one wants while a long task runs,
	// and the mode's other paths already allow it there: mouseSelect scrolls
	// with Up/Down once the input box is frozen, and PageUp/PageDown work
	// everywhere. Only *clicks* are gated — during exec there is no button on
	// screen to answer, and a stray click must not cancel a running task.
	switch msg.Type {
	case tea.MouseWheelUp, tea.MouseWheelDown:
		// delta is the direction of travel: wheel up reads back, so the chat
		// scroll offset grows and the list highlight moves toward the top.
		delta := transcriptScrollStep
		if msg.Type == tea.MouseWheelUp {
			delta = -transcriptScrollStep
		}
		switch m.mode {
		case modeList, modeModelPanel, modeModelWizard, modeOnboarding, modeSkillsHub:
			// A wheel notch pages the highlight through the list (MovePage
			// clamps at both ends); the terms step has no list to move.
			if len(m.selectionList.Items) > 0 {
				m.selectionList.MovePage(delta, m.selectionPageRows())
			}
		default:
			m.scrollTranscript(-delta)
		}
		return m, nil
	}

	if m.mode == modeExec {
		return m, nil
	}

	// Only process left mouse button presses. Ignoring release and motion
	// prevents double-triggering toggle actions (thought preview, stop double-tap).
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// 1. In modeApproving: a click answers the card only when it lands on one
	// of the two options of the choice row, and a click on the scope row only
	// re-picks the remember scope — never answers. Clicks anywhere else — the
	// transcript above, the card body, the frame — are ignored, so a stray
	// click can never approve an irreversible task.
	if m.mode == modeApproving && m.pending != nil {
		choice, scopeHit := m.approvalHit(msg.X, msg.Y)
		if scopeHit >= 0 {
			m.approvalScope = approvalScopes[scopeHit]
			return m, nil
		}
		switch choice {
		case 0:
			return m.approvePending()
		case 1:
			return m.denyPending()
		}
		return m, nil
	}

	// 2. In modeAsking: clicking [⏹ 停止], [⏎ 注入], or [⌃O 思考]
	if m.mode == modeAsking {
		if hit := m.askingButtonHit(msg.X, msg.Y); hit >= 0 {
			switch hit {
			case 0: // Stop
				return m.interrupt()
			case 1: // Steer / Inject
				text := strings.TrimSpace(m.ta.Value())
				if text != "" {
					if m.stream == nil || !m.stream.injectSteer(text) {
						note := block{kind: blockError, body: i18n.T(m.loc, "tui.steer.full")}
						return m, m.printBlock(note)
					}
					m.ta.Reset()
					m.ta.SetHeight(1)
					steerPrefix := i18n.T(m.loc, "tui.turn.steerPrefix")
					m.pendingPrompt += "\n" + steerPrefix + text
					if m.r != nil {
						m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: steerPrefix + text})
					}
					blk := block{
						kind: blockNote,
						body: m.th.accent.Render("💡 ") + i18n.Tf(m.loc, "tui.turn.steered", "idea", text),
					}
					return m, m.printBlock(blk)
				}
			case 2: // Thought
				m.expandThought = !m.expandThought
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}

	// 3. In modeIdle:
	if m.mode == modeIdle {
		// If slash menu is active, clicking on menu rows selects that command or argument
		if m.menu.active && len(m.menu.items) > 0 && m.height > 0 {
			menuOutput := m.menu.render(m.th, m.textWidth(), m.menuRows())
			if menuOutput != "" {
				menuLines := strings.Split(menuOutput, "\n")
				menuCount := len(menuLines)
				// The popup renders directly above statusRow (which is at m.height - 1).
				startRow := m.height - 1 - menuCount
				if msg.Y >= startRow && msg.Y <= m.height-2 && msg.X >= 0 {
					lineIdx := msg.Y - startRow
					start := 0
					rows := m.menuRows()
					if m.menu.sel >= rows {
						start = m.menu.sel - rows + 1
					}
					itemIdx := start + lineIdx
					if itemIdx >= 0 && itemIdx < len(m.menu.items) {
						m.menu.sel = itemIdx
						if m.menu.argMode {
							return m.submit(m.menu.fill())
						}
						return m.submit(m.menu.selected())
					}
				}
			}
		}

		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}

	return m, nil
}

// argResolve adapts the repl's argument resolver for the popup. nil repl (some
// tests) disables argument candidates, leaving command completion intact.
func (m tuiModel) argResolve() argResolver {
	if m.r == nil {
		return nil
	}
	return m.r.argCandidates
}

// onMenuKey handles keystrokes while the slash-command popup is open — over
// command names or, past the first space, over argument candidates. It reports
// handled=false for keys the menu does not claim, so the caller falls through
// to normal editing (typing more of the filter, backspacing, etc.).
func (m tuiModel) onMenuKey(msg tea.KeyMsg) (handled bool, _ tea.Model, _ tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.menu.move(-1)
		return true, m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.menu.move(1)
		return true, m, nil
	case tea.KeyTab:
		// Complete to the highlighted row and leave a trailing space so the
		// next argument can follow; the re-sync re-opens the popup on the new
		// argument position (or closes it when there is nothing to offer).
		if f := m.menu.fill(); f != "" {
			m.ta.SetValue(f)
			m.menu.sync(m.ta.Value(), m.argResolve())
		}
		return true, m, nil
	case tea.KeyEnter:
		// In command mode Enter runs the highlighted command outright — the
		// discovery path. In argument mode it applies the highlighted
		// candidate to the line and submits that: arrows pick, Enter answers.
		if m.menu.argMode {
			if f := m.menu.fill(); f != "" {
				model, cmd := m.submit(f)
				return true, model, cmd
			}
			return false, m, nil
		}
		if sel := m.menu.selected(); sel != "" {
			model, cmd := m.submit(sel)
			return true, model, cmd
		}
		return false, m, nil
	case tea.KeyEsc:
		// First Esc dismisses the popup but keeps the typed line for editing.
		m.menu.close()
		return true, m, nil
	}
	return false, m, nil
}

// submit acts on one submitted line. Quit shortcuts end the program; other
// slash commands and shell escapes ("!cmd") run through the classic dispatch in
// the foreground (see tui_exec.go); anything else is a prompt for the engine.
func (m tuiModel) submit(text string) (tea.Model, tea.Cmd) {
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.menu.close()
	m.scrollOffset = 0
	if text == "/exit" || text == "/quit" {
		m.quitting = true
		return m, tea.Quit
	}

	// Interactive commands rendered in full-screen TUI (Requirement 2, 4, 5)
	switch {
	case text == "/clear" || text == "/cls":
		if m.chatHistory != nil {
			m.chatHistory.blocks = nil
		}
		if m.r != nil {
			m.r.convo = nil
		}
		m.scrollOffset = 0
		return m, nil
	case text == "/sessions" || strings.HasPrefix(text, "/sessions "):
		return m.openSessionsList()
	case text == "/projects" || strings.HasPrefix(text, "/projects "):
		return m.openProjectsList()
	case text == "/resume" || text == "/resume ":
		return m.openResumeList()
	case strings.HasPrefix(text, "/resume "):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/resume"))
		if arg == "-" {
			if m.r != nil {
				m.r.activeSess = ""
			}
			note := block{kind: blockNote, body: i18n.T(m.loc, "repl.resume.detached")}
			return m, m.printBlock(note)
		}
		if m.r != nil && m.r.sessionsSt != nil {
			if sess, err := m.r.sessionsSt.Get(arg); err == nil {
				m.r.activeSess = arg
				if len(sess.Turns) > 0 {
					var convo []entry.Turn
					for _, t := range sess.Turns {
						convo = append(convo, entry.Turn{Role: t.Role, Content: t.Text})
					}
					m.r.convo = convo
					if m.chatHistory != nil {
						m.chatHistory.blocks = nil
						for _, t := range sess.Turns {
							switch t.Role {
							case "user":
								m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: t.Text})
							case "assistant":
								m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockAnswer, body: t.Text})
							}
						}
					}
				}
				note := block{kind: blockNote, body: i18n.Tf(m.loc, "tui.resume.ok", "id", shortID(arg), "title", sess.Title)}
				return m, m.printBlock(note)
			}
		}
		note := block{kind: blockError, body: i18n.Tf(m.loc, "tui.resume.notFound", "id", arg)}
		return m, m.printBlock(note)
	case text == "/model" || strings.HasPrefix(text, "/model"):
		return m.openModelPanel()
	case isSkillsHubInvite(text):
		// "/skills", "/skill", or "... hub" opens the browsable plaza; the
		// verb subcommands (list/find/add/reset/install) keep running through
		// the exec path below.
		return m.openSkillsHub()
	}

	// Slash-mode prefixes pick the turn's interaction mode before the slash
	// dispatcher claims them: "/goal <text>", "/plan <text>", "/spec <text>".
	// A bare mode word just prints its usage line.
	if mode, rest, ok := parseModePrefix(text); ok {
		if rest == "" {
			note := block{kind: blockNote, body: i18n.Tf(m.loc, "tui.mode.usage", "cmd", "/"+mode)}
			return m, m.printBlock(note)
		}
		return m.askTurn(rest, mode)
	}

	if isBareCommand(text) {
		// Other slash/shell commands reuse the repl handlers with request-scoped
		// streams. Output arrives incrementally and this generation alone owns the
		// completion; Esc/Ctrl+C cancels its context without quitting the TUI.
		m.mode = modeExec
		m.execGen++
		m.execText.Reset()
		exec, cmd := startCommandExec(m.r, text, m.execGen)
		m.exec = exec
		return m, cmd
	}

	return m.askTurn(text, "")
}

// isSkillsHubInvite reports whether the line should open the Skills Hub panel
// rather than exec the /skill repl handler.
func isSkillsHubInvite(text string) bool {
	switch text {
	case "/skills", "/skill", "/skills hub", "/skill hub":
		return true
	}
	return false
}

// parseModePrefix splits a "/goal|/plan|/spec <text>" line into its mode and
// remaining text. The slash-mode commands share a prefix rule with the rest of
// the command table: the mode word followed by a space or a line break (so a
// multiline draft can carry a mode). Bare "/goal" et al. also match — rest is
// "" and the caller prints the usage hint.
func parseModePrefix(text string) (mode, rest string, ok bool) {
	for _, p := range []string{"/goal", "/plan", "/spec"} {
		if text == p {
			return p[1:], "", true
		}
		if strings.HasPrefix(text, p+" ") || strings.HasPrefix(text, p+"\n") {
			return p[1:], strings.TrimSpace(text[len(p):]), true
		}
	}
	return "", "", false
}

// askTurn launches one engine turn for prompt with the slash-mode directive
// attached ("" = the classifier decides). It owns every piece of per-turn
// state: transcript echo, file-ref expansion, textarea reset, asking flag.
func (m tuiModel) askTurn(text, mode string) (tea.Model, tea.Cmd) {
	if m.engine == nil {
		if m.r != nil && m.r.engine != nil {
			m.engine = m.r.engine
		} else {
			note := block{
				kind: blockError,
				body: i18n.T(m.loc, "tui.error.noModel"),
			}
			return m, m.printBlock(note)
		}
	}

	m.turnMode = mode

	// Echo the prompt into scrollback so the committed transcript reads as a
	// dialogue, then start the ask. The mode banner rides ahead of the echo so
	// scrollback records which lens the answer was produced under.
	cmds := []tea.Cmd{}
	if mode != "" {
		cmds = append(cmds, m.printBlock(block{kind: blockNote, body: i18n.Tf(m.loc, "tui.mode.banner", "mode", mode)}))
	}
	cmds = append(cmds, m.printBlock(block{kind: blockUser, body: text}))

	// @path references become inline file blocks before the prompt leaves the
	// front end, so "explain @main.go" works without pasting the file. The
	// attachment notices are committed as transcript notes rather than printed,
	// which would land inside the frame Bubble Tea is repainting.
	prompt := text
	if m.r != nil {
		var notes []string
		prompt, notes = m.r.expandFileRefsNotes(text)
		for _, n := range notes {
			cmds = append(cmds, m.printBlock(block{kind: blockNote, body: n}))
		}
	}

	history, workDir := m.history(prompt)
	m.pendingPrompt = prompt
	m.turnWorkDir = workDir
	m.mode = modeAsking
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder.running")
	m.started = time.Now()
	m.lastInterrupt = time.Time{} // each turn gets a fresh double-tap window
	m.liveAnswerChunks = nil
	m.liveAnswerBytes = 0
	m.thought = nil
	m.thoughtDone = false
	m.note = ""
	// The turn reports its own outcome, so the out-of-band watcher holds its
	// tongue until it commits (mirrors the classic loop's setAsking).
	//
	// A repl built without one (some tests) cannot report that it is busy and
	// asks without standing consent, which the approval gate turns into an
	// on-request refusal like any other.
	if m.r != nil {
		m.r.setAsking(true)
	}
	authorize := m.r != nil && m.r.authorize
	sessID := ""
	if m.r != nil {
		sessID = m.r.activeSess
	}

	stream, pump := startAsk(m.engine, history, prompt, workDir, sessID, authorize, mode)
	m.stream = stream
	return m, tea.Batch(append(cmds, m.sp.Tick, pump)...)
}
