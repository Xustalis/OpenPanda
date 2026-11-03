// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A single recorded answer must not crowd every other exchange out of the
// replay window: convoSummaryOf caps the stored text while the full output
// stays reachable through the task row.
func TestConvoSummaryCapsOversizedAnswer(t *testing.T) {
	out := &askengine.Result{Kind: "answer", Answer: strings.Repeat("x", maxConvoAnswerChars+500)}
	got := convoSummaryOf(i18n.English, out)
	if len(got) > maxConvoAnswerChars+100 {
		t.Fatalf("recorded answer = %d chars, want ≤ ~%d", len(got), maxConvoAnswerChars)
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Fatal("recorded answer lost its head")
	}
}

func TestConvoSummaryKeepsNormalAnswer(t *testing.T) {
	out := &askengine.Result{Kind: "answer", Answer: "all good"}
	if got := convoSummaryOf(i18n.English, out); got != "all good" {
		t.Fatalf("recorded answer = %q", got)
	}
}

// A clarify turn is the question the model asked, not "no output": a surface
// without a choice card reads the persisted thread back and must see it.
func TestConvoSummaryRecordsClarifyQuestion(t *testing.T) {
	out := &askengine.Result{Kind: "clarify", Answer: "Which environment should I deploy to?"}
	if got := convoSummaryOf(i18n.English, out); got != out.Answer {
		t.Fatalf("recorded clarify = %q, want the question", got)
	}
}
