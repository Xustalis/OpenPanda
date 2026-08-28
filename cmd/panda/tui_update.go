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
)

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		// The input spans the width minus the frame's border+padding (2+2).
		m.ta.SetWidth(max(20, msg.Width-4))
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
			return m, tea.Println(blk.render(m.th, m.width, m.expandThought))
		}
		return m, nil
	}
	return m, nil
}

// onKey dispatches a keystroke according to the current mode.
func (m tuiModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.mode == modeAsking && m.stream != nil {
			m.stream.cancel() // interrupt the ask; the doneMsg will land with ctx error
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyCtrlO:
		m.expandThought = !m.expandThought
		return m, nil
	}

	switch m.mode {
	case modeAsking:
		if msg.Type == tea.KeyEsc && m.stream != nil {
			m.stream.cancel()
		}
		return m, nil
	case modeApproving:
		return m.onApprovalKey(msg)
	default:
		return m.onIdleKey(msg)
	}
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
	cmds := []tea.Cmd{tea.Println(block{kind: blockUser, body: text}.render(m.th, m.width, m.expandThought))}

	// @path references become inline file blocks before the prompt leaves the
	// front end, so "explain @main.go" works without pasting the file. The
	// attachment notices are committed as transcript notes rather than printed,
	// which would land inside the frame Bubble Tea is repainting.
	prompt := text
	if m.r != nil {
		var notes []string
		prompt, notes = m.r.expandFileRefsNotes(text)
		for _, n := range notes {
			cmds = append(cmds, tea.Println(block{kind: blockNote, body: n}.render(m.th, m.width, m.expandThought)))
		}
	}

	history, workDir := m.history(prompt)
	m.pendingPrompt = prompt
	m.mode = modeAsking
	m.started = time.Now()
	m.liveAnswer.Reset()
	m.thought = nil
	m.thoughtDone = false
	m.note = ""
	// The turn reports its own outcome, so the out-of-band watcher holds its
	// tongue until it commits (mirrors the classic loop's setAsking).
	m.r.setAsking(true)

	stream, pump := startAsk(m.engine, history, prompt, workDir, m.r.authorize)
	m.stream = stream
	return m, tea.Batch(append(cmds, m.sp.Tick, pump)...)
}
