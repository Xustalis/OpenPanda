// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/activity"
)

func TestActivityEvidenceSurvivesMainAndSideReload(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sess, err := store.Create("activity")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := activity.Input(map[string]any{"path": "main.go"})
	records := []activity.Record{{ID: "tool", Name: "workspace_read", Status: "success", Input: input, Output: "display only output"}}
	op := Operation{ID: "main-op", Status: "running", Thought: "display only reasoning", Activities: records}
	if err := store.StartOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "answer", Thought: op.Thought, Activities: records, OperationID: op.ID, IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	op.ID = "side-op"
	if err := store.StartSideOperation(sess.ID, op); err != nil {
		t.Fatal(err)
	}
	op.Status = "done"
	if ok, err := store.FinishSideOperation(sess.ID, op.ID, SideTurn{Role: "assistant", Text: "side answer", Thought: op.Thought, Activities: records}, op); !ok || err != nil {
		t.Fatal(ok, err)
	}
	got, err := NewStore(root).Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Operation.Activities) != 1 || len(got.Turns) != 1 || len(got.Turns[0].Activities) != 1 || got.Turns[0].Thought != op.Thought {
		t.Fatalf("main display lost: %+v", got)
	}
	chat := got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 1 || len(chat.Operation.Activities) != 1 || chat.Operation.Thought != op.Thought || len(chat.Turns[0].Activities) != 1 {
		t.Fatalf("side display lost: %+v", chat)
	}
	if !json.Valid(chat.Turns[0].Activities[0].Input) {
		t.Fatal("activity input corrupted on reload")
	}
}
