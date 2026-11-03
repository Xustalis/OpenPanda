// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"testing"
	"time"
)

// TestSideTurnsStayOutOfTheThread pins the storage contract: a side message is
// durable, never joins Turns, and never moves the thread's UpdatedAt — side
// chatter must not reorder the console's rail.
func TestSideTurnsStayOutOfTheThread(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(sess.ID, Turn{Role: "user", Text: "main question"}); err != nil {
		t.Fatal(err)
	}
	sess, err = st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := sess.UpdatedAt

	time.Sleep(5 * time.Millisecond)
	if _, err := st.AppendSideTurn(sess.ID, SideTurn{Role: "user", Text: "side question"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].Text != "main question" {
		t.Fatalf("a side turn entered the thread: %+v", got.Turns)
	}
	if len(got.SideTurns) != 1 || got.SideTurns[0].Text != "side question" {
		t.Fatalf("side turn not stored: %+v", got.SideTurns)
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Errorf("side turn moved UpdatedAt: %v → %v", updated, got.UpdatedAt)
	}
}

// TestSideOperationCASAndReset covers the side operation's durable
// compare-and-set and the pane's +.
func TestSideOperationCASAndReset(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartSideOperation(sess.ID, Operation{ID: "op1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetSideOperation(sess.ID, Operation{ID: "op2", Status: "done"}); err != nil || ok {
		t.Fatalf("foreign operation id won the CAS (ok=%v err=%v)", ok, err)
	}
	if ok, err := st.SetSideOperation(sess.ID, Operation{ID: "op1", Status: "done"}); err != nil || !ok {
		t.Fatalf("matching operation id lost the CAS (ok=%v err=%v)", ok, err)
	}
	if _, err := st.AppendSideTurn(sess.ID, SideTurn{Role: "assistant", Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResetSide(sess.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideTurns) != 0 || got.SideOperation != nil {
		t.Fatalf("reset left side state behind: turns=%+v op=%+v", got.SideTurns, got.SideOperation)
	}
}

// TestFinishSideOperationIsAtomicAndOwned pins the finalization contract: the
// answer and the settled status land together, and an answer from an operation
// that no longer owns the conversation is dropped.
func TestFinishSideOperationIsAtomicAndOwned(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartSideOperation(sess.ID, Operation{ID: "op1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.FinishSideOperation(sess.ID, "op1",
		SideTurn{Role: "assistant", Text: "answer"},
		Operation{ID: "op1", Status: "done"})
	if err != nil || !ok {
		t.Fatalf("finish: ok=%v err=%v", ok, err)
	}
	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideTurns) != 1 || got.SideTurns[0].Text != "answer" {
		t.Fatalf("answer not filed: %+v", got.SideTurns)
	}
	if got.SideOperation == nil || got.SideOperation.Status != "done" {
		t.Fatalf("operation not settled: %+v", got.SideOperation)
	}

	// A late answer from a replaced operation must not enter the transcript.
	ok, err = st.FinishSideOperation(sess.ID, "op-old",
		SideTurn{Role: "assistant", Text: "late"},
		Operation{ID: "op-old", Status: "done"})
	if err != nil || ok {
		t.Fatalf("stale finish: ok=%v err=%v", ok, err)
	}
	got, _ = st.Get(sess.ID)
	if len(got.SideTurns) != 1 || got.SideTurns[0].Text != "answer" {
		t.Fatalf("stale answer entered the transcript: %+v", got.SideTurns)
	}
}

// TestTrimSideTurnsKeepsWholeExchanges pins the replay window: a trimmed side
// conversation starts on a question, never on a lone answer, and the newest
// exchange always survives.
func TestTrimSideTurnsKeepsWholeExchanges(t *testing.T) {
	turns := []SideTurn{
		{Role: "user", Text: "aaaa"},
		{Role: "assistant", Text: "bbbb"},
		{Role: "user", Text: "cccc"},
		{Role: "assistant", Text: "dddd"},
	}
	kept := TrimSideTurns(turns, 9)
	if len(kept) != 2 || kept[0].Role != "user" || kept[0].Text != "cccc" {
		t.Fatalf("trimmed to %+v, want the newest exchange only", kept)
	}
	if got := TrimSideTurns(turns, 0); len(got) != 4 {
		t.Fatalf("budget 0 should copy everything, got %d", len(got))
	}
	// The input is not mutated through the returned slice.
	kept[0].Text = "changed"
	if turns[2].Text != "cccc" {
		t.Error("TrimSideTurns returned a slice aliasing the stored turns")
	}
}
