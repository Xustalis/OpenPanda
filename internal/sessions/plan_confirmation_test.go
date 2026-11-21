// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import "testing"

func TestLegacyPlanConfirmationLoadsAsReceipt(t *testing.T) {
	store := NewStore(t.TempDir())
	session, err := store.Create("legacy confirmation")
	if err != nil {
		t.Fatal(err)
	}
	internal := "Implement user-confirmed proposal version 7 in the current session.\n\nExact proposal."
	if _, err = store.AppendTurns(session.ID,
		Turn{Role: "user", Text: internal, RoundID: "old-round", TS: 100, IdempotencyKey: "plan:user:old-round"},
		Turn{Role: "user", Text: internal},
	); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("expected two turns, got %d", len(got.Turns))
	}
	receipt := got.Turns[0]
	// The text is the instruction the model keeps replaying; only the label is
	// new. Rewriting it to "/plan confirm 7" would drop the requirements from
	// every later turn's history.
	if receipt.Text != internal || receipt.Kind != planConfirmationKind || receipt.Ref != "7" ||
		receipt.RoundID != "old-round" || receipt.IdempotencyKey != "plan:user:old-round" || receipt.TS != 100 {
		t.Fatalf("unexpected confirmation: %+v", receipt)
	}
	if got.Turns[1].Text != internal || got.Turns[1].Kind != "" {
		t.Fatalf("ordinary user message changed: %+v", got.Turns[1])
	}
}

func TestPlanConfirmationKeepsInstructionForLaterTurns(t *testing.T) {
	instruction := "Implement user-confirmed proposal version 3 in the current session.\n\nShip it."
	turn := PlanConfirmationTurn("round-3", 3, instruction)
	if turn.Text != instruction || turn.Kind != planConfirmationKind || turn.Ref != "3" {
		t.Fatalf("confirmation turn lost the instruction: %+v", turn)
	}
}
