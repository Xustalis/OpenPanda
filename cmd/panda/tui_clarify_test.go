// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func clarifyFixture() entry.ClarifySpec {
	return entry.ClarifySpec{
		Plan: "先改 internal，再跑 make test。",
		Questions: []entry.ClarifyQuestion{
			{
				ID: "scope", Header: "范围", Question: "改哪个目录？",
				Options: []entry.ClarifyOption{
					{Label: "internal", Description: "核心逻辑"},
					{Label: "cmd", Description: "命令行"},
				},
				Recommended: "internal",
			},
			{
				ID: "verify", Header: "验证", Question: "用哪个命令验证？",
				Options: []entry.ClarifyOption{{Label: "make test"}, {Label: "make gate"}},
			},
		},
	}
}

func newClarifyTUI(t *testing.T, spec entry.ClarifySpec) (tuiModel, chan []entry.ClarifyAnswer) {
	t.Helper()
	m := newTestTUI(t)
	reply := make(chan []entry.ClarifyAnswer, 1)
	m.pendingQuestion = newClarifyPrompt(spec, reply)
	m.mode = modeClarifying
	return m, reply
}

func keyRune(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestTUIClarifySingleSelect(t *testing.T) {
	m, reply := newClarifyTUI(t, clarifyFixture())
	m = step(m, keyRune('1')) // internal
	if m.pendingQuestion.index != 1 {
		t.Fatalf("index = %d, want 1 after the first answer", m.pendingQuestion.index)
	}
	m = step(m, keyRune('2')) // make gate
	select {
	case answers := <-reply:
		if len(answers) != 2 {
			t.Fatalf("answers = %+v, want 2", answers)
		}
		if answers[0].Selected[0] != "internal" || answers[1].Selected[0] != "make gate" {
			t.Fatalf("answers = %+v, want internal + make gate", answers)
		}
	default:
		t.Fatal("answers never reached the blocked ask")
	}
	if m.pendingQuestion != nil || m.mode != modeAsking {
		t.Fatalf("pending=%v mode=%v, want the ask resumed", m.pendingQuestion, m.mode)
	}
}

func TestTUIClarifyMultiSelectAndSkip(t *testing.T) {
	spec := clarifyFixture()
	spec.Questions[0].MultiSelect = true
	m, reply := newClarifyTUI(t, spec)
	m = step(m, keyRune('1'))
	m = step(m, keyRune('2'))
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	// Skip the second question: no preference is a valid answer.
	m = step(m, keyRune('s'))
	answers := <-reply
	if len(answers[0].Selected) != 2 {
		t.Fatalf("multi-select = %+v, want both labels", answers[0])
	}
	if len(answers[1].Selected) != 0 || answers[1].Other != "" {
		t.Fatalf("skipped question = %+v, want empty", answers[1])
	}
}

func TestTUIClarifyOtherText(t *testing.T) {
	m, reply := newClarifyTUI(t, clarifyFixture())
	m = step(m, keyRune('o'))
	if m.pendingQuestion == nil || !m.pendingQuestion.other {
		t.Fatal("o did not open the free-text answer")
	}
	m.ta.SetValue("只改 webui")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.pendingQuestion.index != 1 {
		t.Fatalf("index = %d, want 1", m.pendingQuestion.index)
	}
	m = step(m, keyRune('1'))
	answers := <-reply
	if answers[0].Other != "只改 webui" || len(answers[0].Selected) != 0 {
		t.Fatalf("other answer = %+v", answers[0])
	}
}

func TestTUIClarifyCardShowsPlanAndRecommended(t *testing.T) {
	m, _ := newClarifyTUI(t, clarifyFixture())
	m.width = 100
	card := m.clarifyCard()
	for _, want := range []string{"先改 internal", "internal", "(Recommended)", "1/2"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
}

func TestTUIClarifyCancelDropsTheAsk(t *testing.T) {
	m, _ := newClarifyTUI(t, clarifyFixture())
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.pendingQuestion != nil || m.mode != modeIdle {
		t.Fatalf("pending=%v mode=%v, want the round cancelled", m.pendingQuestion, m.mode)
	}
}

// TestTUICommitOpensParkedQuestionCard: a task the agent parked on a
// structured question opens the choice card instead of leaving the user to
// find `panda approve` — the TUI twin of the web's review question card.
func TestTUICommitOpensParkedQuestionCard(t *testing.T) {
	m := newTestTUI(t)
	spec := clarifyFixture()
	res := &askengine.Result{
		Kind: "task", TaskID: "task-1", TaskState: "review",
		Question: "改哪个目录？", QuestionSpec: &spec,
	}
	next, _ := m.commit(res)
	nm := next.(tuiModel)
	if nm.mode != modeClarifying || nm.pendingQuestion == nil {
		t.Fatalf("mode=%v pending=%v, want the question card", nm.mode, nm.pendingQuestion)
	}
	if nm.pendingQuestion.taskID != "task-1" {
		t.Fatalf("taskID = %q, want task-1", nm.pendingQuestion.taskID)
	}
	if len(nm.pendingQuestion.spec.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(nm.pendingQuestion.spec.Questions))
	}
}

func TestTUIClarifyCardShowsPreview(t *testing.T) {
	spec := clarifyFixture()
	spec.Questions[0].Options[0].Preview = "PREVIEW-LAYOUT"
	m, _ := newClarifyTUI(t, spec)
	m.width = 100
	card := m.clarifyCard()
	if !strings.Contains(card, "PREVIEW-LAYOUT") {
		t.Fatalf("focused option preview missing:\n%s", card)
	}
	// The second option has no preview; focusing it drops the block.
	m = step(m, tea.KeyMsg{Type: tea.KeyDown})
	card = m.clarifyCard()
	if strings.Contains(card, "PREVIEW-LAYOUT") {
		t.Fatalf("stale preview after moving focus:\n%s", card)
	}
}

func TestClarifyRecommendedSuffix(t *testing.T) {
	if got := clarifyRecommendedSuffix("internal", i18n.English); got != " (Recommended)" {
		t.Fatalf("plain label suffix = %q", got)
	}
	// A model that already appended the wording must not get it twice.
	if got := clarifyRecommendedSuffix("internal (Recommended)", i18n.English); got != "" {
		t.Fatalf("marked label suffix = %q, want none", got)
	}
	if got := clarifyRecommendedSuffix("内部（推荐）", i18n.ChineseSimp); got != "" {
		t.Fatalf("marked CJK label suffix = %q, want none", got)
	}
}

func TestParseClarifyLine(t *testing.T) {
	q := entry.ClarifyQuestion{
		ID: "q", Header: "h", Question: "which?",
		Options: []entry.ClarifyOption{{Label: "a"}, {Label: "b"}, {Label: "c"}},
	}
	cases := []struct {
		line     string
		multi    bool
		selected []string
		other    string
	}{
		{"1", false, []string{"a"}, ""},
		{"2", false, []string{"b"}, ""},
		{"1,3", true, []string{"a", "c"}, ""},
		{"1,3", false, []string{"a"}, ""},
		{"1 3", true, []string{"a", "c"}, ""},
		{"只改 webui", false, nil, "只改 webui"},
		{"", false, nil, ""},
	}
	for _, tc := range cases {
		qq := q
		qq.MultiSelect = tc.multi
		got := parseClarifyLine(qq, tc.line)
		if len(got.Selected) != len(tc.selected) || got.Other != tc.other {
			t.Fatalf("parseClarifyLine(%q, multi=%v) = %+v, want selected=%v other=%q", tc.line, tc.multi, got, tc.selected, tc.other)
		}
		for i := range tc.selected {
			if got.Selected[i] != tc.selected[i] {
				t.Fatalf("parseClarifyLine(%q) selected = %v, want %v", tc.line, got.Selected, tc.selected)
			}
		}
	}
}
