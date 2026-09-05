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
	"github.com/Xustalis/OpenPanda/internal/entry"
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
			// terminal instead of to a guess (see Init), and the status row can
			// learn which project this run started in.
			m.refreshProject()
			welcome := m.welcome()
			pad := m.height - (len(strings.Split(welcome, "\n")) + len(strings.Split(m.inputView(), "\n")))
			if pad > 0 {
				welcome = strings.Repeat("\n", pad) + welcome
			}
			return m, tea.Println(welcome)
		}
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case spinner.TickMsg:
		m.animTick++
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
		// restored and its output already sits in scrollback. The prompt comes
		// back (the view was blank while the command held the terminal) and the
		// status row re-reads what a command may have just changed — /project,
		// /resume — instead of asking the store on every frame.
		m.mode = modeIdle
		m.applyLocale() // /lang may have switched the session language mid-run
		m.refreshProject()
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
	// A foreground command owns the terminal and the view is blank; Bubble Tea is
	// not reading input, so anything that reaches us here is not ours to act on.
	if m.mode == modeExec {
		return m, nil
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
	case modeAsking:
		if msg.Type == tea.KeyEsc {
			return m.interrupt()
		}
		if msg.Type == tea.KeyEnter {
			text := strings.TrimSpace(m.ta.Value())
			if text == "" {
				return m, nil
			}
			m.ta.Reset()
			m.ta.SetHeight(1)
			m.pendingPrompt += "\n[补充想法/Steering]: " + text
			if m.stream != nil {
				m.stream.injectSteer(text)
			}
			if m.r != nil {
				m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "[补充想法/Steering]: " + text})
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
	// Re-filter the popup against the edited line: it opens on a bare "/token",
	// then follows the argument position — "/lang " lists locale codes, the
	// token under the cursor filters them — and closes on a non-slash line.
	m.menu.sync(m.ta.Value(), m.argResolve())
	return m, cmd
}

// onMouse handles terminal mouse events (clicks) across all modes.
func (m tuiModel) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeExec || m.quitting {
		return m, nil
	}

	// Only process left mouse button presses. Ignoring release and motion
	// prevents double-triggering toggle actions (thought preview, stop double-tap).
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// 1. In modeApproving: a click answers the card only when it lands on one
	// of the two options of the choice row. Clicks anywhere else — the
	// transcript above, the card body, the frame — are ignored, so a stray
	// click can never approve an irreversible task.
	if m.mode == modeApproving && m.pending != nil {
		switch m.approvalHit(msg.X, msg.Y) {
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
					m.ta.Reset()
					m.ta.SetHeight(1)
					m.pendingPrompt += "\n[补充想法/Steering]: " + text
					if m.stream != nil {
						m.stream.injectSteer(text)
					}
					if m.r != nil {
						m.r.convo = append(m.r.convo, entry.Turn{Role: "user", Content: "[补充想法/Steering]: " + text})
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
	if text == "/exit" || text == "/quit" {
		m.quitting = true
		return m, tea.Quit
	}
	if isBareCommand(text) {
		// Slash/shell commands reuse the repl handlers verbatim; their output
		// flows to scrollback while Bubble Tea has the terminal released. The mode
		// blanks the frame in the same event-loop pass that queues the exec, which
		// is what stops the released terminal from stranding a copy of the input
		// box above the command's output (see modeExec).
		m.mode = modeExec
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
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder.running")
	m.started = time.Now()
	m.lastInterrupt = time.Time{} // each turn gets a fresh double-tap window
	m.liveAnswer = ""
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
