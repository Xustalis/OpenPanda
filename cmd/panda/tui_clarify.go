// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// clarifyPrompt is the pre-execution choice card: the questions the entry
// model asked before it will classify a task, the cursor, and the channel the
// blocked ask goroutine is waiting on. Questions are answered one at a time so
// a four-question round stays readable on a narrow terminal.
type clarifyPrompt struct {
	spec    entry.ClarifySpec
	index   int
	sel     int
	picked  map[string]bool // multi-select: labels toggled for the current question
	answers []entry.ClarifyAnswer
	// other is true while the free-text answer is being typed; the main input
	// box is the editor, so a question can be answered in the user's words.
	other     bool
	otherText string
	reply     chan<- []entry.ClarifyAnswer
}

func newClarifyPrompt(spec entry.ClarifySpec, reply chan<- []entry.ClarifyAnswer) *clarifyPrompt {
	return &clarifyPrompt{spec: spec, picked: map[string]bool{}, reply: reply}
}

// current returns the question being answered, or nil when the round is done
// (or malformed — validation rejects an empty round before it reaches here).
func (q *clarifyPrompt) current() *entry.ClarifyQuestion {
	if q == nil || q.index < 0 || q.index >= len(q.spec.Questions) {
		return nil
	}
	return &q.spec.Questions[q.index]
}

// onClarifyKey routes every keystroke while the choice card is up. Options use
// the same picker vocabulary as the approval card (arrows, digits, enter); a
// question can be skipped with s or answered in free text with o.
func (m tuiModel) onClarifyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := m.pendingQuestion
	if q == nil {
		m.mode = modeIdle
		return m, nil
	}
	cur := q.current()
	if cur == nil {
		m.pendingQuestion = nil
		m.mode = modeAsking
		return m, nil
	}
	if q.other {
		return m.onClarifyOtherKey(msg)
	}
	switch key := strings.ToLower(msg.String()); key {
	case "esc":
		return m.cancelClarify()
	case "s":
		return m.answerClarify(entry.ClarifyAnswer{ID: cur.ID})
	case "o":
		q.other = true
		q.otherText = ""
		m.ta.SetValue("")
		m.ta.Placeholder = i18n.T(m.loc, "tui.clarify.otherHint")
		return m, nil
	case "up", "k", "shift+tab":
		if q.sel > 0 {
			q.sel--
		}
		return m, nil
	case "down", "j", "tab":
		if q.sel < len(cur.Options)-1 {
			q.sel++
		}
		return m, nil
	case "enter":
		if cur.MultiSelect {
			return m.answerMultiClarify(cur)
		}
		return m.answerClarify(entry.ClarifyAnswer{ID: cur.ID, Selected: []string{cur.Options[q.sel].Label}})
	case "1", "2", "3", "4":
		idx := int(key[0] - '1')
		if idx < 0 || idx >= len(cur.Options) {
			return m, nil
		}
		q.sel = idx
		if cur.MultiSelect {
			label := cur.Options[idx].Label
			q.picked[label] = !q.picked[label]
			return m, nil
		}
		return m.answerClarify(entry.ClarifyAnswer{ID: cur.ID, Selected: []string{cur.Options[idx].Label}})
	case " ":
		if cur.MultiSelect && q.sel < len(cur.Options) {
			label := cur.Options[q.sel].Label
			q.picked[label] = !q.picked[label]
		}
		return m, nil
	}
	return m, nil
}

// onClarifyOtherKey handles the free-text sub-mode. The textarea is the
// editor; enter commits the typed answer and esc returns to the option list
// without losing the picks already made.
func (m tuiModel) onClarifyOtherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := m.pendingQuestion
	cur := q.current()
	if cur == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		q.other = false
		m.ta.SetValue("")
		m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder.running")
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(m.ta.Value())
		q.other = false
		m.ta.SetValue("")
		m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder.running")
		if text == "" {
			return m, nil
		}
		answer := entry.ClarifyAnswer{ID: cur.ID, Other: text}
		if cur.MultiSelect {
			for _, o := range cur.Options {
				if q.picked[o.Label] {
					answer.Selected = append(answer.Selected, o.Label)
				}
			}
		}
		return m.answerClarify(answer)
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.ta.SetHeight(min(8, max(1, m.ta.LineCount())))
	return m, cmd
}

// answerMultiClarify confirms a multi-select question with the toggled labels.
// An empty selection is a valid "no preference" answer and is passed through
// as such; the model falls back to its recommendation.
func (m tuiModel) answerMultiClarify(cur *entry.ClarifyQuestion) (tea.Model, tea.Cmd) {
	q := m.pendingQuestion
	var sel []string
	for _, o := range cur.Options {
		if q.picked[o.Label] {
			sel = append(sel, o.Label)
		}
	}
	return m.answerClarify(entry.ClarifyAnswer{ID: cur.ID, Selected: sel})
}

// answerClarify records one question's answer and either advances to the next
// question or releases the blocked ask goroutine with the complete set.
func (m tuiModel) answerClarify(a entry.ClarifyAnswer) (tea.Model, tea.Cmd) {
	q := m.pendingQuestion
	q.answers = append(q.answers, a)
	q.index++
	q.sel = 0
	q.picked = map[string]bool{}
	q.other = false
	if q.index < len(q.spec.Questions) {
		return m, nil
	}
	// Buffered channel: the ask goroutine is already parked on it, and this
	// send must never block the UI thread.
	q.reply <- q.answers
	m.pendingQuestion = nil
	m.mode = modeAsking
	m.ta.SetValue("")
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder.running")
	m.started = time.Now()
	return m, nil
}

// cancelClarify abandons the round: the ask is dropped (its context is
// cancelled, which unblocks the engine's question callback), and the
// transcript records the same quiet interruption note as Esc mid-answer.
func (m tuiModel) cancelClarify() (tea.Model, tea.Cmd) {
	m.pendingQuestion = nil
	m.mode = modeIdle
	m.resetLive()
	if m.stream != nil {
		m.stream.drop()
		m.stream = nil
	}
	m.ta.SetValue("")
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder")
	done := m.turnEnded()
	blk := block{kind: blockNote, body: i18n.T(m.loc, "repl.interrupted")}
	return m.withPending(done, m.printBlock(blk))
}

// clarifyCard renders the pre-execution choice card: the optional plan, the
// current question, its options and the key hints.
func (m tuiModel) clarifyCard() string {
	q := m.pendingQuestion
	cur := q.current()
	if cur == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.th.warn.Render(m.th.glyph("⚠", "!") + " " + i18n.T(m.loc, "clarify.title")))
	if plan := strings.TrimSpace(q.spec.Plan); plan != "" {
		sb.WriteString("\n" + plan)
	}
	header := strings.TrimSpace(cur.Header)
	title := i18n.Tf(m.loc, "tui.clarify.question",
		"i", fmt.Sprint(q.index+1), "n", fmt.Sprint(len(q.spec.Questions)))
	sb.WriteString("\n\n" + m.th.heading.Render(title) + " " + m.th.muted.Render("["+header+"]"))
	sb.WriteString("\n" + cur.Question)
	for i, o := range cur.Options {
		label := o.Label
		if label == cur.Recommended {
			label += " " + i18n.T(m.loc, "clarify.recommended")
		}
		if d := strings.TrimSpace(o.Description); d != "" {
			label += m.th.muted.Render(" — " + d)
		}
		mark := "  "
		if cur.MultiSelect && q.picked[o.Label] {
			mark = m.th.accent.Render(m.th.glyph("◼", "[x]")) + " "
		}
		if i == q.sel {
			mark += m.th.heading.Render(m.th.glyph("❯", ">") + " ")
		} else {
			mark += "  "
		}
		sb.WriteString("\n" + mark + m.th.command.Render(fmt.Sprintf("[%d]", i+1)) + " " + label)
	}
	hintKey := "tui.clarify.hint"
	if cur.MultiSelect {
		hintKey = "tui.clarify.hintMulti"
	}
	if q.other {
		hintKey = "tui.clarify.otherHint"
	}
	sb.WriteString("\n" + m.th.muted.Render(i18n.T(m.loc, hintKey)))
	return m.th.approval.Width(max(1, m.textWidth()-2)).Render(sb.String())
}
