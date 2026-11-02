// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// newSideTestModel builds an idle TUI with an already-open side channel. The
// channel's ask is not started: these tests cover the surface (keys, rendering,
// isolation from the main transcript), not the engine, which
// internal/askengine/side_test.go covers against a fake model.
func newSideTestModel(t *testing.T) tuiModel {
	t.Helper()
	m := newIsolatedTUI(t)
	ta := textarea.New()
	ta.SetWidth(80)
	ta.Focus()
	m.side = &sideState{
		ch: &chatHistory{},
		ta: ta,
	}
	m.sideVisible = true
	return m
}

// TestSideEscapeReturnsWithoutDropping pins the return semantics: Esc is a way
// back, not a close. The channel — including its turns and a question still in
// flight — must be exactly as it was when the user comes back.
func TestSideEscapeReturnsWithoutDropping(t *testing.T) {
	m := newSideTestModel(t)
	m.side.turns = []entry.Turn{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
	}

	next, _ := m.onSideKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)

	if m.sideVisible {
		t.Error("Esc should return to the main thread")
	}
	if m.side == nil {
		t.Fatal("Esc must not discard the channel")
	}
	if len(m.side.turns) != 2 {
		t.Errorf("side turns were lost on return: %+v", m.side.turns)
	}
}

// TestSideEscapeClearsDraftFirst is the two-step Esc the main composer also
// uses: an editor with text in it clears before anything else happens, so a
// mistyped side question does not throw the channel away.
func TestSideEscapeClearsDraftFirst(t *testing.T) {
	m := newSideTestModel(t)
	m.side.ta.SetValue("半句话")

	next, _ := m.onSideKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)

	if !m.sideVisible {
		t.Error("Esc on a non-empty draft should only clear it")
	}
	if got := m.side.ta.Value(); got != "" {
		t.Errorf("draft not cleared: %q", got)
	}
}

// TestSideCloseDiscardsState pins the explicit close: the channel and
// everything in it are gone, and the main composer takes the keyboard back.
func TestSideCloseDiscardsState(t *testing.T) {
	m := newSideTestModel(t)
	m.side.ch.blocks = append(m.side.ch.blocks, block{kind: blockAnswer, body: "旧回答"})

	next, _ := submitSideLine(m, "/side close")
	m = next.(tuiModel)

	if m.side != nil || m.sideVisible {
		t.Error("/side close must discard the channel")
	}
	if !m.ta.Focused() {
		t.Error("the main composer should regain focus after the channel closes")
	}
}

// TestSideCopyPutsAnswerInComposer is the export path: the last answer becomes
// a draft on the main thread, unsent. The channel closes behind it, because the
// user's next move is to edit and send.
func TestSideCopyPutsAnswerInComposer(t *testing.T) {
	m := newSideTestModel(t)
	m.side.ch.blocks = append(m.side.ch.blocks,
		block{kind: blockUser, body: "这三个参数分别是什么"},
		block{kind: blockAnswer, body: "第一个是超时，第二个是重试次数。"},
	)

	next, _ := submitSideLine(m, "/side copy")
	m = next.(tuiModel)

	if got := m.ta.Value(); !strings.Contains(got, "第一个是超时") {
		t.Errorf("composer did not receive the answer: %q", got)
	}
	if m.sideVisible {
		t.Error("/side copy should land the user back on the main thread")
	}
}

// TestSideAnswerStaysOutOfMainTranscript is the isolation invariant: a finished
// side exchange lives in the channel's own chatHistory and its own turns. A
// leak here would make the main thread carry a conversation its own history
// does not contain — the next ask would replay the wrong thing.
func TestSideAnswerStaysOutOfMainTranscript(t *testing.T) {
	m := newSideTestModel(t)
	before := len(m.chatHistory.blocks)

	stream := &askStream{events: make(chan tea.Msg, 8)}
	m.side.stream = stream
	m.side.answer = []string{"侧边", "的回答"}
	next, _ := m.Update(doneMsg{stream: stream, out: &askengine.Result{Kind: "answer", Answer: "侧边的回答"}})
	m = next.(tuiModel)

	if got := len(m.chatHistory.blocks); got != before {
		t.Errorf("side answer leaked into the main transcript (%d → %d blocks)", before, got)
	}
	var answers int
	for _, b := range m.side.ch.blocks {
		if b.kind == blockAnswer && b.body == "侧边的回答" {
			answers++
		}
	}
	if answers != 1 {
		t.Errorf("the side answer was not committed to the channel: %+v", m.side.ch.blocks)
	}
	if len(m.side.turns) != 1 || m.side.turns[0].Role != "assistant" {
		t.Errorf("the answer did not join the channel's replay history: %+v", m.side.turns)
	}
}

// TestSideAnswerKeepsToolTrace pins the display note under an answer: what the
// channel checked stays on screen, and it stays out of the replay history — a
// note about a tool call is not something the next question should carry.
func TestSideAnswerKeepsToolTrace(t *testing.T) {
	m := newSideTestModel(t)
	stream := &askStream{events: make(chan tea.Msg, 8)}
	m.side.stream = stream
	next, _ := m.Update(progressMsg{stream: stream, progress: askengine.Progress{
		Kind: askengine.ProgressTool, Name: "workspace_read", Detail: "internal/askengine/side.go",
	}})
	m = next.(tuiModel)
	next, _ = m.Update(doneMsg{stream: stream, out: &askengine.Result{Kind: "answer", Answer: "读完了"}})
	m = next.(tuiModel)

	var trace string
	for _, b := range m.side.ch.blocks {
		if b.kind == blockNote && strings.Contains(b.body, "workspace_read") {
			trace = b.body
		}
	}
	if !strings.Contains(trace, "internal/askengine/side.go") {
		t.Errorf("the answer's tool trace is missing: %+v", m.side.ch.blocks)
	}
	for _, turn := range m.side.turns {
		if strings.Contains(turn.Content, "workspace_read") {
			t.Errorf("the trace leaked into the replay history: %+v", m.side.turns)
		}
	}
}

// TestSideLineDoesNotRecordWithoutEngine guards the no-model case: a question
// typed with no engine to ask must not leave a dangling user turn in the
// channel's replay history.
func TestSideLineDoesNotRecordWithoutEngine(t *testing.T) {
	m := newSideTestModel(t)
	next, _ := submitSideLine(m, "没有模型时的问题")
	m = next.(tuiModel)

	if len(m.side.turns) != 0 {
		t.Errorf("a question with no engine to answer it was recorded: %+v", m.side.turns)
	}
	if m.side.status == "" {
		t.Error("the channel should say why nothing was asked")
	}
}

// submitSideLine types one line into the channel's composer and presses Enter,
// the way a user does — the handler reads the textarea, not the key's runes.
func submitSideLine(m tuiModel, line string) (tea.Model, tea.Cmd) {
	m.side.ta.SetValue(line)
	return m.onSideKey(tea.KeyMsg{Type: tea.KeyEnter})
}

// TestSideViewRenders checks the frame draws the channel's own content and
// nothing from the main transcript — the visual half of the same invariant.
func TestSideViewRenders(t *testing.T) {
	m := newSideTestModel(t)
	m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: "主线程里的话"})
	m.side.ch.blocks = append(m.side.ch.blocks, block{kind: blockAnswer, body: "侧边的回答"})

	view := m.View()
	if !strings.Contains(view, "侧边的回答") {
		t.Errorf("side view is missing the side content:\n%s", view)
	}
	if strings.Contains(view, "主线程里的话") {
		t.Errorf("side view leaked main-thread content:\n%s", view)
	}
}

// TestSideProgressStaysOnItsOwnStatusLine guards the two-streams-at-once case:
// while a main turn is running, a side ask's routing notes must not overwrite
// the main status line (and the main turn's must not overwrite the side's).
func TestSideProgressStaysOnItsOwnStatusLine(t *testing.T) {
	m := newSideTestModel(t)
	sideStream := &askStream{events: make(chan tea.Msg, 8)}
	m.side.stream = sideStream
	mainStream := &askStream{events: make(chan tea.Msg, 8)}
	m.stream = mainStream

	next, _ := m.Update(progressMsg{stream: sideStream, progress: askengine.Progress{Kind: askengine.ProgressRoute, Name: "side-model"}})
	m = next.(tuiModel)
	if m.side.status == "" {
		t.Error("the side stream's progress did not reach the side status line")
	}
	if m.note != "" {
		t.Errorf("the side stream's progress leaked into the main note: %q", m.note)
	}
}
