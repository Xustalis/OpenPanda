// SPDX-License-Identifier: AGPL-3.0-or-later

package taskproviders

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/testdb"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

func TestLocalMutationConcurrentReplayAndConflict(t *testing.T) {
	p := Local{Store: todos.NewStore(testdb.Open(t))}
	ctx := context.Background()
	in := todos.CreateInput{Title: "one intention"}
	var wg sync.WaitGroup
	results := make(chan todos.Todo, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { v, e := p.CreateLocal(ctx, in, "one-key"); results <- v; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id int64
	for v := range results {
		if id == 0 {
			id = v.ID
		}
		if v.ID != id || v.Revision != 1 {
			t.Fatalf("replay: %+v", v)
		}
	}
	if _, e := p.CreateLocal(ctx, todos.CreateInput{Title: "different"}, "one-key"); !errors.Is(e, ErrConflict) {
		t.Fatalf("key/body conflict: %v", e)
	}
	rows, e := p.Store.List(ctx, todos.Filter{})
	if e != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v %v", rows, e)
	}
	if _, e := p.CreateLocal(ctx, in, ""); !errors.Is(e, ErrPrecondition) {
		t.Fatalf("missing key: %v", e)
	}
}

func TestLocalPatchCASAndExactReplay(t *testing.T) {
	p := Local{Store: todos.NewStore(testdb.Open(t))}
	ctx := context.Background()
	original, e := p.CreateLocal(ctx, todos.CreateInput{Title: "original"}, "create")
	if e != nil {
		t.Fatal(e)
	}
	revision := LocalSnapshot(original).TaskRevision
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, title := range []string{"first", "second"} {
		wg.Go(func() {
			_, e := p.PatchLocal(ctx, original.ID, revision, todos.Patch{Title: &title}, "patch-"+title)
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("CAS success=%d conflict=%d", ok, conflict)
	}
	current, e := p.Store.Get(ctx, original.ID)
	if e != nil {
		t.Fatal(e)
	}
	if current.Revision <= original.Revision {
		t.Fatal("revision did not advance")
	}
	replay, e := p.CreateLocal(ctx, todos.CreateInput{Title: "original"}, "create")
	if e != nil || replay.Title != "original" || replay.Revision != 1 {
		t.Fatalf("response replay: %+v %v", replay, e)
	}
	title := "legacy edit"
	if _, e = p.Store.Patch(ctx, original.ID, todos.Patch{Title: &title}); e != nil {
		t.Fatal(e)
	}
	if _, e = p.PatchLocal(ctx, original.ID, LocalSnapshot(current).TaskRevision, todos.Patch{Title: &title}, "stale"); !errors.Is(e, ErrConflict) {
		t.Fatalf("legacy edit must invalidate revision: %v", e)
	}
}
