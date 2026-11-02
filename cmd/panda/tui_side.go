// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// The side channel: a read-only conversation beside the main thread, opened
// with /side and dismissed with Esc. It exists because the everyday interruption
// is a question, not a task — "what does this function do", "which card claims
// that", "did the plan mention X" — and until now the only way to ask one was
// to type it into the main thread, where it either steered the running turn or
// became the next turn of a thread it had nothing to do with.
//
// The state lives here and nowhere else: no session file, no turn written to
// the parent. Process exit discards it, which is the design (a persisted side
// thread is just a /fork with extra steps). Three boundaries hold it apart from
// the main thread:
//
//   - Context. The history is the parent thread as it stood when the channel
//     opened, and it is re-read before every side question: the main thread
//     may keep moving, and a question about the present is answered from the
//     present thread, not from the opening snapshot.
//   - Dispatch. The engine's side scope has no task_submit and answers a
//     classified task or plan with a fixed note (internal/askengine/side.go).
//   - Rendering. Side blocks go into their own chatHistory, never into
//     m.chatHistory, so returning to the main thread shows it unchanged.

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// sideState is everything the channel owns. It is allocated on first /side and
// kept across hide/show, which is what makes "leave, do something, come back"
// land in the same conversation.
type sideState struct {
	// history is the parent thread as it stood when the channel opened. It is
	// refreshed from the session before every side ask, so each question reads
	// the thread the user has in mind rather than the one they left behind;
	// the channel's own turns are kept separately below the boundary marker.
	history []entry.Turn
	// workDir and sessionID mirror the main thread's, so the side ask resolves
	// files and approvals the same way. sessionID carries a ":side" suffix — a
	// remembered approval granted here must not silently answer for the main
	// thread.
	workDir   string
	sessionID string

	// turns is the side conversation itself: one user question, one assistant
	// answer, appended pairwise. It is replayed after history on every ask.
	turns []entry.Turn

	// ch holds the committed side Q&A for rendering. It is a chatHistory like
	// the main one — same block renderers, same markdown — but a separate
	// instance, so nothing here can reach the main transcript.
	ch *chatHistory

	ta textarea.Model

	// stream is the in-flight side ask, nil between questions.
	stream *askStream
	// answer/thought accumulate the live stream. They become a block on done.
	answer  []string
	thought []string
	// status is the one-line progress note (routing, retrying, tool calls).
	status string
	// tools is what this answer checked, in order, for the note committed
	// under it. Display only — it never joins the replay history.
	tools []string
}

// sideSessionSuffix separates the side channel's approval bucket from the
// thread it was opened on.
const sideSessionSuffix = ":side"

// sideEnabled reports whether a side channel exists and is on screen.
func (m tuiModel) sideShowing() bool {
	return m.sideVisible && m.side != nil
}

// openSide shows the channel, creating it on first use. arg is the optional
// question from "/side <question>"; an empty arg just brings the composer up.
func (m *tuiModel) openSide(arg string) tea.Cmd {
	if m.r == nil || m.engine == nil {
		// No engine to ask. Opening a composer that cannot send would be a
		// trap, so say why and stay put — same words the bare REPL uses.
		return m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "repl.ask.noEngine")})
	}
	if m.side == nil {
		hist, workDir := m.sideSnapshot()
		ta := textarea.New()
		ta.Placeholder = i18n.T(m.loc, "tui.side.placeholder")
		ta.Prompt = m.th.accent.Render(m.th.glyph("❯", ">")) + " "
		ta.ShowLineNumbers = false
		ta.CharLimit = 0
		ta.MaxHeight = 6
		ta.SetHeight(1)
		ta.KeyMap.InsertNewline.SetKeys("enter", "ctrl+m", "ctrl+j")
		ta.FocusedStyle.CursorLine = lipglossNoStyle()
		ta.BlurredStyle.CursorLine = lipglossNoStyle()
		m.side = &sideState{
			history:   hist,
			workDir:   workDir,
			sessionID: m.sideSessionID(),
			ch:        &chatHistory{},
			ta:        ta,
		}
	}
	m.sideVisible = true
	m.ta.Blur()
	m.side.ta.Focus()
	m.sizeSideInput()
	if q := strings.TrimSpace(arg); q != "" {
		return m.askSide(q)
	}
	// The channel is up and its composer holds focus; the cursor blink is the
	// command, the same one the main composer arms on submit.
	return textarea.Blink
}

// closeSide takes the channel off screen. The side ask, if one is running, is
// left alone: it finishes in the background and its answer is waiting the next
// time the channel opens. Esc is a return, not a cancel.
func (m *tuiModel) closeSide() tea.Cmd {
	m.sideVisible = false
	if m.side != nil {
		m.side.ta.Blur()
	}
	m.ta.Focus()
	return textarea.Blink
}

// dropSide discards the channel entirely (the explicit close). A running ask is
// cancelled: the user asked for the whole thing to go away.
func (m *tuiModel) dropSide() {
	if m.side != nil && m.side.stream != nil {
		m.side.stream.drop()
	}
	m.side = nil
	m.sideVisible = false
	m.ta.Focus()
}

// sizeSideInput keeps the side textarea the width of the frame. It runs on
// resize and on open, since the textarea was built without geometry.
func (m *tuiModel) sizeSideInput() {
	if m.side == nil {
		return
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	m.side.ta.SetWidth(max(1, w-6))
}

// sideSessionID derives the approval bucket for this channel from the main
// thread's. An unbound main thread (bare REPL) has no thread id, and the side
// channel gets its own constant bucket rather than the shared session-less one.
func (m tuiModel) sideSessionID() string {
	id := ""
	if m.r != nil {
		id = m.r.sessID()
	}
	if id == "" {
		return "side" + sideSessionSuffix
	}
	return id + sideSessionSuffix
}

// sideSnapshot reads the parent thread without touching it. Unlike
// repl.askContext this never appends a turn: a side question is not part of the
// thread, and writing it there would make the next main turn carry a question
// the user asked elsewhere.
func (m tuiModel) sideSnapshot() ([]entry.Turn, string) {
	if m.r == nil {
		return nil, ""
	}
	if id := m.r.sessID(); id != "" && m.r.sessionsSt != nil {
		if sess, err := m.r.sessionsSt.Get(id); err == nil {
			_, hist := sessionHistory(context.Background(), m.r.sessionsSt, sess, m.engine)
			workDir := sess.Worktree
			if workDir == "" && m.engine != nil {
				workDir = m.engine.WorkPath()
			}
			return hist, workDir
		}
	}
	return m.r.convoNow(), ""
}

// askSide submits one question to the channel. A second question while one is
// still streaming is refused rather than queued: the side channel is a place to
// read, and an answer racing another answer is not worth the model cost.
func (m *tuiModel) askSide(question string) tea.Cmd {
	if m.side == nil {
		return nil
	}
	if m.engine == nil {
		// Nothing to ask. Say so on the channel's own status line instead of
		// recording a turn that can never be answered.
		m.side.status = i18n.T(m.loc, "repl.ask.noEngine")
		return nil
	}
	if m.side.stream != nil {
		m.side.status = i18n.T(m.loc, "tui.side.busy")
		return nil
	}
	s := m.side
	// Re-read the thread before every question. The channel opens with a
	// snapshot, but the main thread may have moved since: an answer about
	// "where are we now" built from an hour-old snapshot is exactly the dead
	// end the channel exists to avoid. The channel's own turns stay below the
	// boundary marker; only the reference material above it refreshes.
	if hist, wd := m.sideSnapshot(); len(hist) > 0 || wd != "" {
		s.history = hist
		if wd != "" {
			s.workDir = wd
		}
	}
	// prior is the channel's conversation before this question. It travels to
	// the engine separately from the main-thread snapshot: the boundary marker
	// goes between them.
	prior := s.turns
	s.turns = append(s.turns, entry.Turn{Role: "user", Content: question})
	s.ch.blocks = append(s.ch.blocks, block{kind: blockUser, body: question})
	s.answer = nil
	s.thought = nil
	s.tools = nil
	s.status = i18n.T(m.loc, "tui.side.asking")
	team := []string(nil)
	if m.r != nil {
		team = m.r.teamForAsk()
	}
	stream, pump := startSideAsk(
		m.engine,
		s.history,
		prior,
		question,
		s.workDir,
		s.sessionID,
		team,
		m.nextNode,
		m.r != nil && m.r.authorized(),
	)
	m.side.stream = stream
	return tea.Batch(pump, m.sp.Tick)
}

// onSideDelta folds a streamed chunk into the channel's live answer.
func (m *tuiModel) onSideDelta(text string) tea.Cmd {
	if m.side == nil {
		return nil
	}
	m.side.answer = append(m.side.answer, text)
	return waitForActivity(m.side.stream)
}

// onSideDone commits the finished answer into the channel's transcript.
func (m *tuiModel) onSideDone(msg doneMsg) tea.Cmd {
	s := m.side
	if s == nil {
		return nil
	}
	s.stream = nil
	s.status = ""
	text := strings.TrimSpace(strings.Join(s.answer, ""))
	s.answer = nil
	s.thought = nil

	switch {
	case msg.err != nil && errors.Is(msg.err, context.Canceled):
		s.ch.blocks = append(s.ch.blocks, block{kind: blockNote, body: i18n.T(m.loc, "repl.interrupted")})
	case msg.err != nil:
		if text != "" {
			s.ch.blocks = append(s.ch.blocks, block{kind: blockAnswer, body: text})
		}
		s.ch.blocks = append(s.ch.blocks, block{kind: blockError, body: msg.err.Error()})
	case msg.out != nil && msg.out.Answer != "":
		text = msg.out.Answer
		s.ch.blocks = append(s.ch.blocks, block{kind: blockAnswer, body: text})
	default:
		s.ch.blocks = append(s.ch.blocks, block{kind: blockNote, body: i18n.T(m.loc, "tui.side.empty")})
	}
	// The committed answer is part of the channel's own history, so the next
	// question in this thread sees what the previous one answered.
	if text != "" {
		s.turns = append(s.turns, entry.Turn{Role: "assistant", Content: text})
	}
	// What the answer checked stays visible under it: the reader can act on a
	// task id or a file the model actually read instead of taking prose on
	// faith. It is a display note, not a turn — the next ask does not replay it.
	if len(s.tools) > 0 {
		s.ch.blocks = append(s.ch.blocks, block{kind: blockNote, body: i18n.Tf(m.loc, "tui.side.checked", "tools", strings.Join(s.tools, " · "))})
		s.tools = nil
	}
	return nil
}

// onSideKey routes keys while the channel is on screen. It takes over the
// whole keyboard — the main thread's bindings (Ctrl+C to quit, Esc to
// interrupt) would otherwise fire on the wrong thing.
func (m tuiModel) onSideKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.side
	if s == nil {
		m.sideVisible = false
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		// Esc clears a draft first, then returns. It never cancels the side
		// ask: Esc is the way back, and a question in flight should be waiting
		// when the user returns.
		if strings.TrimSpace(s.ta.Value()) != "" {
			s.ta.Reset()
			s.ta.SetHeight(1)
			return m, nil
		}
		return m, m.closeSide()
	case tea.KeyCtrlC:
		// Ctrl+C is the explicit stop for the side ask, and a second one
		// closes the channel — the same two-step the main thread uses.
		if s.stream != nil {
			s.stream.drop()
			s.stream = nil
			s.status = ""
			s.answer = nil
			s.ch.blocks = append(s.ch.blocks, block{kind: blockNote, body: i18n.T(m.loc, "repl.interrupted")})
			return m, nil
		}
		if strings.TrimSpace(s.ta.Value()) != "" {
			s.ta.Reset()
			s.ta.SetHeight(1)
			return m, nil
		}
		m.dropSide()
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(s.ta.Value())
		if text == "" {
			return m, nil
		}
		s.ta.Reset()
		s.ta.SetHeight(1)
		if strings.HasPrefix(text, "/side") {
			return m, m.sideCommand(strings.TrimSpace(strings.TrimPrefix(text, "/side")))
		}
		return m, m.askSide(text)
	}
	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(msg)
	s.ta.SetHeight(min(6, max(1, s.ta.LineCount())))
	return m, cmd
}

// sideCommand handles the /side subcommands typed inside the channel.
func (m *tuiModel) sideCommand(arg string) tea.Cmd {
	switch arg {
	case "close", "exit", "q":
		m.dropSide()
		return nil
	case "copy", "insert", "draft":
		// Put the last answer into the main composer as a draft. Nothing is
		// sent: what to do with the conclusion is the user's call, and the
		// whole point of the channel is that it does not touch the thread.
		if m.side != nil {
			if last := m.side.lastAnswer(); last != "" {
				m.ta.SetValue(last)
				return m.closeSide()
			}
		}
		return nil
	case "":
		return nil
	}
	if m.side != nil {
		m.side.status = i18n.T(m.loc, "tui.side.usage")
	}
	return nil
}

// lastAnswer returns the most recent committed assistant answer in the channel.
func (s *sideState) lastAnswer() string {
	for i := len(s.ch.blocks) - 1; i >= 0; i-- {
		if s.ch.blocks[i].kind == blockAnswer {
			return s.ch.blocks[i].body
		}
	}
	return ""
}

// sideView renders the channel full screen: a title bar, the committed side
// Q&A, the live answer, and the composer. It replaces the main chat frame
// rather than overlaying it — the terminal has one scroll region, and a card
// drawn over a live transcript is unreadable the moment either one moves.
func (m tuiModel) sideView() string {
	s := m.side
	if s == nil {
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	h = max(1, h-1)
	inner := max(1, w-4)

	var rows []string

	// Title bar: what this surface is, and the one key that leaves it.
	title := m.th.heading.Render(i18n.T(m.loc, "tui.side.title"))
	hint := m.th.muted.Render(i18n.T(m.loc, "tui.side.escape"))
	gap := inner - lipgloss.Width(title) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	rows = append(rows, " "+title+strings.Repeat(" ", gap)+hint+" ")

	var body []string
	if len(s.ch.blocks) > 0 {
		for i := range s.ch.blocks {
			body = append(body, "")
			body = append(body, s.ch.renderedAt(i, m.th, inner, m.expandThought, m.expandHints)...)
		}
	}

	// Live region: reasoning fold, streaming answer, spinner. It is what the
	// user watches between Enter and the committed block.
	if live := m.sideLive(inner); live != "" {
		body = append(body, "")
		body = append(body, strings.Split(live, "\n")...)
	}

	// Composer: the same bordered box as the main frame, so the channel reads
	// as another room in the same house rather than a different program.
	box := m.th.inputBox
	if s.stream != nil {
		box = m.th.inputBoxRunning.BorderForeground(m.th.breathingColor(m.animTick))
	}
	composer := []string{"", box.Width(max(1, inner-2)).Render(s.ta.View())}
	if s.status != "" {
		composer = append(composer, m.th.muted.Render("  "+s.status))
	}

	footer := m.th.muted.Render("  " + i18n.T(m.loc, "tui.side.footer"))

	budget := h - len(rows) - len(composer) - 1 // -1 for the footer
	if budget < 1 {
		budget = 1
	}
	if len(body) > budget {
		body = body[len(body)-budget:]
	}

	rows = append(rows, body...)
	rows = append(rows, composer...)
	rows = append(rows, footer)
	return strings.Join(rows, "\n")
}

// sideLive renders the in-flight part of the channel at the channel's own
// width. It deliberately does not reuse streamAnswerText: that memo is keyed to
// the main frame's width, and the channel's content column is narrower.
func (m tuiModel) sideLive(width int) string {
	s := m.side
	if s == nil {
		return ""
	}
	var b strings.Builder
	if len(s.thought) > 0 && m.expandThought {
		b.WriteString(m.th.italic.Render(strings.Join(s.thought, "")))
		b.WriteString("\n")
	}
	if len(s.answer) > 0 {
		b.WriteString(answerText(m.th, strings.Join(s.answer, ""), width))
	} else if s.stream != nil {
		b.WriteString(m.sp.View())
	}
	return strings.TrimRight(b.String(), "\n")
}
