// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
)

func TestAppendTurnIdempotentAcrossStores(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sess, err := store.Create("durable report")
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{Role: "assistant", Kind: "answer", Text: "completed", IdempotencyKey: "summary:task:attempt"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := NewStore(root).AppendTurn(sess.ID, turn); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].IdempotencyKey != turn.IdempotencyKey || got.Turns[0].TS == 0 {
		t.Fatalf("replayed turns = %+v, want one persisted, stamped report", got.Turns)
	}
	before, err := os.ReadFile(store.path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate restart and a producer rebuilding different text for the same
	// delivery. The first durable turn wins without a new timestamp/write.
	turn.Text = "different replay"
	if _, err := NewStore(root).AppendTurn(sess.ID, turn); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("duplicate delivery rewrote the transcript")
	}
}

func TestAppendTurnsMixedIdempotentBatch(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("mixed turns")
	if err != nil {
		t.Fatal(err)
	}
	first := Turn{Role: "assistant", Text: "attempt one", IdempotencyKey: "attempt:1"}
	if _, err := store.AppendTurn(sess.ID, first); err != nil {
		t.Fatal(err)
	}
	got, err := store.AppendTurns(sess.ID,
		first,
		Turn{Role: "user", Text: "repeat"},
		Turn{Role: "user", Text: "repeat"},
		Turn{Role: "assistant", Text: "attempt two", IdempotencyKey: "attempt:2"},
		Turn{Role: "assistant", Text: "duplicate in batch", IdempotencyKey: "attempt:2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 4 || got.Turns[3].Text != "attempt two" {
		t.Fatalf("mixed batch = %+v, want two keyed reports plus both unkeyed turns", got.Turns)
	}
}

func TestIdempotencySurvivesCompactionAndFork(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sess, err := store.Create("compacted reports")
	if err != nil {
		t.Fatal(err)
	}
	report := Turn{Role: "assistant", Text: "durable task outcome", IdempotencyKey: "summary:task:attempt"}
	if _, err := store.AppendTurns(sess.ID, Turn{Role: "user", Text: "old question"}, report, Turn{Role: "user", Text: "new question"}, Turn{Role: "assistant", Text: "new answer"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.CompactHistory(context.Background(), sess.ID, 25, func(context.Context, []Turn) (string, error) { return "Earlier task completed.", nil })
	if err != nil || len(got.Turns) != 2 || len(got.CompactedTurnKeys) != 1 {
		t.Fatalf("compaction lost key: %+v %v", got, err)
	}
	if _, err := NewStore(root).AppendTurn(sess.ID, report); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(sess.ID)
	if err != nil || len(got.Turns) != 2 {
		t.Fatalf("compacted report resurrected: %+v %v", got, err)
	}
	child, err := store.Fork(sess.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root).AppendTurn(child.ID, report); err != nil {
		t.Fatal(err)
	}
	child, err = store.Get(child.ID)
	if err != nil || len(child.Turns) != 2 || child.Summary == "" {
		t.Fatalf("fork forgot inherited acknowledgement: %+v %v", child, err)
	}
}
