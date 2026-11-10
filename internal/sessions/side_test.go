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
	chat := got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 1 || chat.Turns[0].Text != "side question" {
		t.Fatalf("side turn not stored: %+v", got.SideChats)
	}
	if chat.Title != "side question" {
		t.Errorf("side chat title = %q, want the first question", chat.Title)
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
	if len(got.SideChats) != 0 || got.SideActive != "" {
		t.Fatalf("reset left side state behind: chats=%+v active=%q", got.SideChats, got.SideActive)
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
	chat := got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 1 || chat.Turns[0].Text != "answer" {
		t.Fatalf("answer not filed: %+v", got.SideChats)
	}
	if chat.Operation == nil || chat.Operation.Status != "done" {
		t.Fatalf("operation not settled: %+v", chat.Operation)
	}

	// A late answer from a replaced operation must not enter the transcript.
	ok, err = st.FinishSideOperation(sess.ID, "op-old",
		SideTurn{Role: "assistant", Text: "late"},
		Operation{ID: "op-old", Status: "done"})
	if err != nil || ok {
		t.Fatalf("stale finish: ok=%v err=%v", ok, err)
	}
	got, _ = st.Get(sess.ID)
	chat = got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 1 || chat.Turns[0].Text != "answer" {
		t.Fatalf("stale answer entered the transcript: %+v", got.SideChats)
	}
}

// TestSideChatsStaySeparate pins the multi-conversation contract: a thread can
// hold several side chats, each with its own transcript and operation, and
// neither the question nor the answer crosses between them.
func TestSideChatsStaySeparate(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.AppendSideTurn(sess.ID, SideTurn{Role: "user", Text: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.SideActive

	second, err := st.NewSideChat(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.SideActive == firstID {
		t.Fatalf("new chat did not move the active pointer: %q", second.SideActive)
	}
	secondID := second.SideActive
	if _, err := st.AppendSideTurn(sess.ID, SideTurn{Role: "user", Text: "second question"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideChats) != 2 {
		t.Fatalf("chats = %d, want 2", len(got.SideChats))
	}
	one := got.SideChatByID(firstID)
	two := got.SideChatByID(secondID)
	if one == nil || two == nil {
		t.Fatalf("missing chat in %+v", got.SideChats)
	}
	if len(one.Turns) != 1 || one.Turns[0].Text != "first question" {
		t.Errorf("first chat = %+v", one.Turns)
	}
	if len(two.Turns) != 1 || two.Turns[0].Text != "second question" {
		t.Errorf("second chat = %+v", two.Turns)
	}

	// An answer belongs to the chat that asked: the operation id names it, so
	// a reader that switched away does not receive it in another transcript.
	if err := st.StartSideOperationFor(sess.ID, firstID, Operation{ID: "op-a", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartSideOperationFor(sess.ID, secondID, Operation{ID: "op-b", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinishSideOperation(sess.ID, "op-a",
		SideTurn{Role: "assistant", Text: "first answer"},
		Operation{ID: "op-a", Status: "done"}); err != nil || !ok {
		t.Fatalf("finish first: ok=%v err=%v", ok, err)
	}
	got, _ = st.Get(sess.ID)
	if c := got.SideChatByID(firstID); len(c.Turns) != 2 || c.Turns[1].Text != "first answer" {
		t.Errorf("first chat after its answer = %+v", c.Turns)
	}
	if c := got.SideChatByID(secondID); len(c.Turns) != 1 {
		t.Errorf("the answer leaked into the other chat: %+v", c.Turns)
	}

	// Selecting moves the pointer, not the transcripts.
	got, err = st.SelectSideChat(sess.ID, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SideActive != firstID || len(got.ActiveSideChat().Turns) != 2 {
		t.Fatalf("select did not land on the first chat: %+v", got.SideActive)
	}

	// Deleting one leaves the other, and the pointer moves off the deleted id.
	got, err = st.DeleteSideChat(sess.ID, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideChats) != 1 || got.SideChats[0].ID != secondID {
		t.Fatalf("delete left %+v", got.SideChats)
	}
	if got.SideActive != secondID {
		t.Errorf("active pointer = %q, want the surviving chat", got.SideActive)
	}
}

// TestLegacySideTurnsBecomeOneChat pins the migration: a file written before
// side chats were a list keeps its transcript, and the next write persists the
// new shape.
func TestLegacySideTurnsBecomeOneChat(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Session{
		ID: sess.ID, Title: sess.Title, CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		Turns:         []Turn{},
		SideTurns:     []SideTurn{{Role: "user", Text: "old question", TS: 100}, {Role: "assistant", Text: "old answer", TS: 101}},
		SideOperation: &Operation{ID: "op-old", Status: "done"},
	}
	if err := st.save(legacy); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	chat := got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 2 || chat.Turns[0].Text != "old question" {
		t.Fatalf("legacy transcript lost: %+v", got.SideChats)
	}
	if chat.ID != legacySideChatID || chat.Title != "old question" {
		t.Errorf("legacy chat = %+v", chat)
	}
	if chat.Operation == nil || chat.Operation.ID != "op-old" {
		t.Errorf("legacy operation lost: %+v", chat.Operation)
	}

	// A write persists the list and drops the legacy fields.
	if _, err := st.AppendSideTurn(sess.ID, SideTurn{Role: "user", Text: "new question"}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Get(sess.ID)
	if len(got.SideChats) != 1 || len(got.SideChats[0].Turns) != 3 {
		t.Fatalf("post-migration chats = %+v", got.SideChats)
	}
	if len(got.SideTurns) != 0 {
		t.Errorf("legacy turns survived the write: %+v", got.SideTurns)
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
