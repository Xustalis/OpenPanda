// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A needs-changed-input park refuses approve. The hint must name the verb that
// actually works (`task continue`), not send the user into the refusal.
func TestTaskReviewHintMatchesDisposition(t *testing.T) {
	needsInput := core.Task{State: core.StateReview, ApprovalDisposition: core.ApprovalNeedsChangedInput}
	hint := taskReviewHint(i18n.English, needsInput, "01abc")
	if !strings.Contains(hint, "task continue 01abc") {
		t.Fatalf("needs-input hint = %q, want the task continue command", hint)
	}
	if strings.Contains(hint, "panda approve") {
		t.Fatalf("needs-input hint still suggests approve: %q", hint)
	}

	resumable := core.Task{State: core.StateReview, ApprovalDisposition: core.ApprovalResumeExecution}
	hint = taskReviewHint(i18n.English, resumable, "01abc")
	if !strings.Contains(hint, "panda approve 01abc") {
		t.Fatalf("resumable hint = %q, want the approve command", hint)
	}
}

func TestLatestReviewReasonReadsNewestReview(t *testing.T) {
	events := []core.Event{
		{Type: core.EvReview, DataJSON: `{"reason":"first"}`},
		{Type: core.EvResult, DataJSON: `{"failed":"not a review"}`},
		{Type: core.EvReview, DataJSON: `{"reason":"second, the full reason"}`},
	}
	if got := latestReviewReason(events); got != "second, the full reason" {
		t.Fatalf("latestReviewReason = %q", got)
	}
	if got := latestReviewReason(nil); got != "" {
		t.Fatalf("empty events reason = %q, want empty", got)
	}
}
