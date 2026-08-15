package ctxstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/xenith/panda/internal/storage"
)

func openStoreDB(t *testing.T, max int) *Store {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db, max)
}

func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openStoreDB(t, 0)

	if err := s.Put(ctx, "h1", "file", []byte("hello"), []string{"a"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	e, ok, err := s.Get(ctx, "h1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if e.Type != "file" || string(e.Data) != "hello" || len(e.Refs) != 1 || e.Refs[0] != "a" {
		t.Fatalf("round-trip mismatch: %+v", e)
	}

	if ok, _ := s.Contains(ctx, "h1"); !ok {
		t.Fatalf("contains should be true")
	}
	if _, ok, _ := s.Get(ctx, "missing"); ok {
		t.Fatalf("get missing should be not-ok")
	}
}

func TestPutUpsertsInPlace(t *testing.T) {
	ctx := context.Background()
	s := openStoreDB(t, 0)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	must(s.Put(ctx, "h1", "file", []byte("v1"), nil))
	must(s.Put(ctx, "h1", "file", []byte("v2"), nil))

	e, ok, _ := s.Get(ctx, "h1")
	if !ok || string(e.Data) != "v2" {
		t.Fatalf("upsert did not update in place: %+v", e)
	}
}

func TestLRUEviction(t *testing.T) {
	ctx := context.Background()
	s := openStoreDB(t, 2) // cap 2

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	must(s.Put(ctx, "a", "file", []byte("a"), nil))
	must(s.Put(ctx, "b", "file", []byte("b"), nil))
	// Touch "a" so "b" becomes the least-recently-used.
	_, _, _ = s.Get(ctx, "a")

	// Inserting "c" must evict "b" (least recently used), not "a".
	must(s.Put(ctx, "c", "file", []byte("c"), nil))

	if ok, _ := s.Contains(ctx, "a"); !ok {
		t.Fatalf("recently-accessed 'a' was wrongly evicted")
	}
	if ok, _ := s.Contains(ctx, "b"); ok {
		t.Fatalf("least-recently-used 'b' was not evicted")
	}
	if ok, _ := s.Contains(ctx, "c"); !ok {
		t.Fatalf("new 'c' missing")
	}
}

func TestUnlimitedStoreNeverEvicts(t *testing.T) {
	ctx := context.Background()
	s := openStoreDB(t, 0) // 0 = unlimited

	for i := 0; i < 10; i++ {
		if err := s.Put(ctx, hashOf(t, i), "file", []byte("x"), nil); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	for i := 0; i < 10; i++ {
		if ok, _ := s.Contains(ctx, hashOf(t, i)); !ok {
			t.Fatalf("unlimited store evicted %d", i)
		}
	}
}

func hashOf(t *testing.T, i int) string {
	t.Helper()
	h, _, err := Pack(Snapshot{Type: "file", Data: json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`)})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	return h
}

func TestMaxEntriesForResourceClass(t *testing.T) {
	cases := map[string]int{
		"Micro": 5, "Standard": 50, "Full": 0, "Weird": 50,
	}
	for rc, want := range cases {
		if got := MaxEntriesForResourceClass(rc); got != want {
			t.Fatalf("MaxEntries(%q) = %d, want %d", rc, got, want)
		}
	}
}

func TestPackHashDeterministic(t *testing.T) {
	a := Snapshot{Type: "file", Data: json.RawMessage(`{"repo":"/r"}`)}
	b := Snapshot{Type: "file", Data: json.RawMessage(`{"repo":"/r"}`)}
	c := Snapshot{Type: "file", Data: json.RawMessage(`{"repo":"/other"}`)}

	ha, _, _ := Pack(a)
	hb, _, _ := Pack(b)
	hc, _, _ := Pack(c)

	if ha != hb {
		t.Fatalf("identical snapshots must hash equal: %s vs %s", ha, hb)
	}
	if ha == hc {
		t.Fatalf("distinct snapshots must hash differently")
	}
	if Hash([]byte("x")) != Hash([]byte("x")) {
		t.Fatalf("Hash must be deterministic")
	}
}

// TestConcurrentPutsRespectCap hammers Put from multiple goroutines with the
// store over capacity, verifying the transactional upsert+evict (P2-14) never
// errors and never leaves the store above its cap.
func TestConcurrentPutsRespectCap(t *testing.T) {
	ctx := context.Background()
	s := openStoreDB(t, 10)

	var wg sync.WaitGroup
	errs := make(chan error, 128)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 16; i++ {
				h := fmt.Sprintf("h-%d-%d", w, i)
				if err := s.Put(ctx, h, "file", []byte("data"), nil); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent put: %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM context`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count > 10 {
		t.Fatalf("count = %d, want <= 10 after concurrent puts", count)
	}
}
