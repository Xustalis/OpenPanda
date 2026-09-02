package main

// Update: the model's event loop. It routes keystrokes by mode and folds engine
// events (delta/reasoning/progress/done) into the in-flight turn. Committed
// output is pushed to scrollback with tea.Println; the ephemeral View holds only
// the live region and the input box.

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		first := !m.ready
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		// The input spans the width minus the frame's border+padding (2+2).
		m.ta.SetWidth(max(20, msg.Width-4))
		if first {
			// First size report: now the welcome frame can be drawn to the real
			// terminal instead of to a guess (see Init).
			return m, tea.Println(m.welcome())
		}
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)

	case spinner.TickMsg:
		if m.mode != modeAsking {
			return m, nil
		}
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case deltaMsg:
		return m.onDelta(string(msg))
	case reasoningMsg:
		m.thought = appendReasoning(m.thought, string(msg))
		return m, waitForActivity(m.stream)
	case progressMsg:
		return m.onProgress(askengine.Progress(msg))
	case doneMsg:
		return m.onDone(msg)
	case resumedMsg:
		return m.onResumed(msg)
	case watchMsg:
		return m.onWatch(msg)
	case execDoneMsg:
		// A slash/shell command finished in the foreground; the terminal is
		// restored and its output already sits in scrollback. Surface a rare
		// dispatch error as a transcript block, otherwise just resume idle.
		if msg.err != nil {
			blk := block{kind: blockError, body: msg.err.Error()}
			return m, m.printBlock(blk)
		}
		return m, nil
	case droppedMsg:
		// The pump's ask was released while it was parked. There is nothing to
		// fold in and the pump is not re-armed, which is what ends it.
		return m, nil
	}
	return m, nil
}

// interruptWindow is how long a second Esc/Ctrl-C during a turn counts as
// "quit" rather than a second cancel. It mirrors the classic loop's window so
// the two front ends answer the same keystrokes the same way.
const interruptWindow = time.Second

// onKey dispatches a keystroke according to the current mode.
func (m tuiModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	case modeAsking:
		if msg.Type == tea.KeyEsc {
			return m.interrupt()
		}
		return m, nil
	case modeApproving:
		return m.onApprovalKey(msg)
	default:
		return m.onIdleKey(msg)
	}
}

// interrupt answers Esc/Ctrl-C during a turn. It releases this front end from
// the ask; it does not stop the work. Once the engine hands a task to the core,
// the core owns that task's lifetime — submitTask runs under the engine's own
// context, not the ask's — so the task runs to completion and the out-of-band
// watcher announces it here when it lands. That is why the late doneMsg is
// dropped rather than committed (see onDone) and why the note says the task
// keeps going instead of claiming it stopped.
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
		// A ResumeApproved re-run is in flight. It has no stream to release and
		// the engine runs it under its own context, so stay in the turn and say
		// plainly what the key did not do — leaving the spinner up is more
		// honest than returning to a prompt while work continues.
		note := block{kind: blockNote, body: i18n.T(m.loc, "tui.turn.busy")}
		return m, m.printBlock(note)
	}

	m.stream.drop()
	m.mode = modeIdle
	m.stream = nil
	m.resetLive()
	note := block{kind: blockNote, body: i18n.T(m.loc, "tui.turn.detached")}
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
	switch msg.Type {
	case tea.KeyEnter:
		text := strings.TrimSpace(m.ta.Value())
		if text == "" {
			return m, nil
		}
		return m.submit(text)
	case tea.KeyEsc:
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
	// Re-filter the popup against the edited line: it opens on a bare "/token"
	// and closes once a space (arguments) or a non-slash line is typed.
	m.menu.sync(m.ta.Value())
	return m, cmd
}

// onMenuKey handles keystrokes while the slash-command popup is open. It reports
// handled=false for keys the menu does not claim, so the caller falls through to
// normal editing (typing more of the filter, backspacing, etc.).
func (m tuiModel) onMenuKey(msg tea.KeyMsg) (handled bool, _ tea.Model, _ tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.menu.move(-1)
		return true, m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.menu.move(1)
		return true, m, nil
	case tea.KeyTab:
		// Complete the token to the highlighted command and leave a trailing
		// space so arguments can follow; the space closes the popup on re-sync.
		if sel := m.menu.selected(); sel != "" {
			m.ta.SetValue(sel + " ")
			m.menu.sync(m.ta.Value())
		}
		return true, m, nil
	case tea.KeyEnter:
		// Enter runs the highlighted command outright — the discovery path.
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
	if text == "/exit" || text == "/quit" {
		m.quitting = true
		return m, tea.Quit
	}
	if isBareCommand(text) {
		// Slash/shell commands reuse the repl handlers verbatim; their output
		// flows to scrollback while Bubble Tea has the terminal released.
		return m, m.runSlash(text)
	}
	// Echo the prompt into scrollback so the committed transcript reads as a
	// dialogue, then start the ask.
	cmds := []tea.Cmd{m.printBlock(block{kind: blockUser, body: text})}

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
	m.started = time.Now()
	m.lastInterrupt = time.Time{} // each turn gets a fresh double-tap window
	m.liveAnswer.Reset()
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

	stream, pump := startAsk(m.engine, history, prompt, workDir, authorize)
	m.stream = stream
	return m, tea.Batch(append(cmds, m.sp.Tick, pump)...)
}
