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
