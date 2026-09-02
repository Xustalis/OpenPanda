package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestResolveTaskIDExactAndPrefix covers the reference forms every CLI listing
// hands back to the user: the full id it stores, and the short prefix it prints.
func TestResolveTaskIDExactAndPrefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := createTask(t, s, "", "resolve me", "node")

	got, err := s.ResolveTaskID(ctx, tk.TaskID)
	if err != nil || got != tk.TaskID {
		t.Fatalf("exact id: got %q err %v", got, err)
	}

	// The first dash-group is what `panda queue` shows; it has to work as a ref.
	short := tk.TaskID[:8]
	got, err = s.ResolveTaskID(ctx, short)
	if err != nil || got != tk.TaskID {
		t.Fatalf("prefix %q: got %q err %v", short, got, err)
	}
}

// TestResolveTaskIDAmbiguous is the case that forbids a newest-wins guess:
// UUIDv7 puts a millisecond timestamp in the leading bytes, so two tasks created
// together share their short id. Acting on the wrong task is worse than asking,
// so the collision must surface with its candidates.
func TestResolveTaskIDAmbiguous(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := createTask(t, s, "", "first", "node")
	b := createTask(t, s, "", "second", "node")

	shared := commonPrefix(a.TaskID, b.TaskID)
	if len(shared) < 4 {
		t.Skipf("ids do not share a usable prefix (%s / %s)", a.TaskID, b.TaskID)
	}

	_, err := s.ResolveTaskID(ctx, shared)
	var amb *AmbiguousTaskIDError
	if !errors.As(err, &amb) {
		t.Fatalf("want AmbiguousTaskIDError, got %v", err)
	}
	if len(amb.Candidates) != 2 {
		t.Fatalf("want both candidates, got %v", amb.Candidates)
	}
	if amb.Ref != shared {
		t.Fatalf("ref not carried: %q", amb.Ref)
	}
}

// TestResolveTaskIDMissing keeps "no such task" distinguishable from "ambiguous":
// the CLI maps the two to different exit codes and different advice.
func TestResolveTaskIDMissing(t *testing.T) {
	s := newTestStore(t)
	for _, ref := range []string{"", "nope", "01a0deadbeef"} {
		if _, err := s.ResolveTaskID(context.Background(), ref); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ref %q: want ErrNoRows, got %v", ref, err)
		}
	}
}

// TestTaskStatesCoversTheVocabulary guards the list `panda queue --state` shows
// against drifting from the constants it is meant to mirror.
func TestTaskStatesCoversTheVocabulary(t *testing.T) {
	for _, st := range []string{
		StateSubmitted, StateQueued, StateDispatched, StateWaitingCtx,
		StateRunning, StateReview, StateDone, StateFailed,
		StateCancelled, StateExpired,
	} {
		if !IsTaskState(st) {
			t.Errorf("state %q missing from TaskStates()", st)
		}
	}
	if IsTaskState("pending") {
		t.Error("pending is not a task state")
	}
}

func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}
