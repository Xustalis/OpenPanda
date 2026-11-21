// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// A confirmation written by the build that stored "/plan confirm N" must still
// reach the model as the instruction, rebuilt from the stored plan run.
func TestHistoryTextRestoresReceiptOnlyConfirmation(t *testing.T) {
	e, _, ctx := newStageTestEngine(t)
	const sid = "restore-session"
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO conversation_plan_runs(session_id,version,proposal,round_id,status) VALUES(?,?,?,?,?)`,
		sid, 4, "Ship the exact fixture", "round-4", "done"); err != nil {
		t.Fatal(err)
	}
	receipt := sessions.Turn{
		Role: "user", Kind: "plan_confirmation", Ref: "4",
		Text: "/plan confirm 4", RoundID: "round-4",
		IdempotencyKey: "plan:user:round-4",
	}
	got := e.HistoryText(ctx, sid, receipt)
	if strings.HasPrefix(got, "/plan confirm") || !strings.Contains(got, "Ship the exact fixture") ||
		!strings.Contains(got, "version 4") {
		t.Fatalf("receipt was not restored: %q", got)
	}

	// A turn that already carries the instruction is returned unchanged.
	full := sessions.Turn{Role: "user", Kind: "plan_confirmation", Ref: "4", Text: "Implement it fully", RoundID: "round-4"}
	if e.HistoryText(ctx, sid, full) != full.Text {
		t.Fatal("a complete instruction must not be rewritten")
	}

	// A receipt whose run is gone degrades to its stored text rather than failing.
	orphan := sessions.Turn{Role: "user", Kind: "plan_confirmation", Ref: "99", Text: "/plan confirm 99", RoundID: "round-99"}
	if e.HistoryText(ctx, sid, orphan) != orphan.Text {
		t.Fatal("a missing plan run must fall back to the stored text")
	}

	// Ordinary user text is untouched.
	plain := sessions.Turn{Role: "user", Text: "hello"}
	if e.HistoryText(ctx, sid, plain) != "hello" {
		t.Fatal("ordinary text must not be rewritten")
	}
}
