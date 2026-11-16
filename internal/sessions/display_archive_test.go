// SPDX-License-Identifier: AGPL-3.0-or-later
package sessions

import (
	"fmt"
	"os"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/activity"
)

func TestFullDisplayArchiveSurvivesPreviewTruncationAndRestart(t *testing.T) {
	for _, side := range []bool{false, true} {
		t.Run(fmt.Sprint(side), func(t *testing.T) {
			root := t.TempDir()
			s := NewStore(root)
			sess, _ := s.Create("archive")
			op := Operation{ID: "op", Status: "running"}
			if side {
				_ = s.StartSideOperation(sess.ID, op)
			} else {
				_ = s.StartOperation(sess.ID, op)
			}
			for i := 0; i < activity.MaxRecords+20; i++ {
				rec := activity.Record{ID: fmt.Sprint(i), Name: "shell", Sequence: i, Status: "success", Output: fmt.Sprint(i)}
				if err := s.AppendDisplay(sess.ID, "op", &rec, ""); err != nil {
					t.Fatal(err)
				}
				op.Activities = activity.Upsert(op.Activities, rec)
			}
			if err := s.AppendDisplay(sess.ID, "op", nil, "actual reasoning"); err != nil {
				t.Fatal(err)
			}
			op.Status = "done"
			if side {
				_, _ = s.FinishSideOperation(sess.ID, "op", SideTurn{Role: "assistant", Text: "done", Activities: op.Activities}, op)
			} else {
				_, _ = s.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "done", OperationID: "op", Activities: op.Activities})
				_, _ = s.SetOperation(sess.ID, op)
			}
			reopened := NewStore(root)
			got, err := reopened.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			records := got.Turns
			if side {
				chat := got.ActiveSideChat()
				if len(chat.Turns[0].Activities) != activity.MaxRecords+20 || chat.Operation.Thought != "actual reasoning" {
					t.Fatal("side archive clipped")
				}
			} else if len(records[0].Activities) != activity.MaxRecords+20 || got.Operation.Thought != "actual reasoning" {
				t.Fatal("main archive clipped")
			}
			if err := reopened.AppendDisplay(sess.ID, "op", nil, "late"); err == nil {
				t.Fatal("terminal generation accepted late activity")
			}
		})
	}
}

func TestArchiveCrashTailAndGenerationFence(t *testing.T) {
	s := NewStore(t.TempDir())
	sess, _ := s.Create("crash")
	_ = s.StartOperation(sess.ID, Operation{ID: "one", Status: "running"})
	rec := activity.Record{ID: "call", Name: "shell", Status: "running"}
	if err := s.AppendDisplay(sess.ID, "one", &rec, ""); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(s.displayPath(sess.ID, "one"), os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("{partial")
	_ = f.Close()
	got, err := NewStore(s.root).Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Operation.Activities) != 2 || !got.Operation.Activities[1].Truncated {
		t.Fatal("damaged tail not marked")
	}
	_ = s.StartOperation(sess.ID, Operation{ID: "two", Status: "running"})
	if err := s.AppendDisplay(sess.ID, "one", &rec, ""); err == nil {
		t.Fatal("stale generation wrote archive")
	}
}

func TestMissingDisplayArchiveRemainsVisibleAfterNextOperation(t *testing.T) {
	for _, side := range []bool{false, true} {
		t.Run(fmt.Sprint(side), func(t *testing.T) {
			s := NewStore(t.TempDir())
			sess, _ := s.Create("missing archive")
			op := Operation{ID: "archived", Status: "running"}
			if side {
				_ = s.StartSideOperation(sess.ID, op)
			} else {
				_ = s.StartOperation(sess.ID, op)
			}
			record := activity.Record{ID: "call", Name: "shell", Status: "success", Output: "full output"}
			if err := s.AppendDisplay(sess.ID, op.ID, &record, "summary"); err != nil {
				t.Fatal(err)
			}
			// The producer's older snapshot must not erase the archive marker.
			if side {
				_, _ = s.SetSideOperation(sess.ID, op)
				op.Status = "done"
				_, _ = s.FinishSideOperation(sess.ID, op.ID, SideTurn{Role: "assistant", Text: "done"}, op)
				_ = s.StartSideOperation(sess.ID, Operation{ID: "next", Status: "running"})
			} else {
				_, _ = s.SetOperation(sess.ID, op)
				_, _ = s.AppendTurn(sess.ID, Turn{Role: "assistant", Text: "done", OperationID: op.ID})
				_ = s.StartOperation(sess.ID, Operation{ID: "next", Status: "running"})
			}
			if err := os.Remove(s.displayPath(sess.ID, op.ID)); err != nil {
				t.Fatal(err)
			}
			got, err := NewStore(s.root).Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			var records []activity.Record
			if side {
				records = got.ActiveSideChat().Turns[0].Activities
			} else {
				records = got.Turns[0].Activities
			}
			if len(records) != 1 || records[0].ID != "archive-unavailable" {
				t.Fatalf("missing archive silently disappeared: %+v", records)
			}
		})
	}
}

func TestForkOwnsDisplayArchiveAfterParentDeletion(t *testing.T) {
	s := NewStore(t.TempDir())
	parent, _ := s.Create("parent")
	op := Operation{ID: "display-source", Status: "running"}
	if err := s.StartOperation(parent.ID, op); err != nil {
		t.Fatal(err)
	}
	record := activity.Record{ID: "archived-call", Name: "shell", Status: "success", Output: "archive evidence"}
	if err := s.AppendDisplay(parent.ID, op.ID, &record, "observed summary"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendTurn(parent.ID, Turn{Role: "assistant", Text: "done", OperationID: op.ID}); err != nil {
		t.Fatal(err)
	}
	child, err := s.Fork(parent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(s.root).Get(child.ID)
	if err != nil || len(got.Turns) != 1 || len(got.Turns[0].Activities) != 1 || got.Turns[0].Thought != "observed summary" {
		t.Fatalf("fork lost durable evidence: %+v %v", got, err)
	}
	if _, err := os.Stat(s.displayPath(parent.ID, op.ID)); !os.IsNotExist(err) {
		t.Fatal("deleted session archive retained")
	}
}
