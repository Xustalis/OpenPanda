package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xenith/panda/internal/ctxstore"
	"github.com/xenith/panda/internal/ledger"
)

// TestPackContextLevels covers the packContext decision table in isolation:
// summary (no snapshot), pointer (file+repo auto-pack), and passthrough (an
// explicit pre-packed hash+level).
func TestPackContextLevels(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "pack", "")

	hash, level, err := c.packContext(ctx, TaskInput{ContextType: "command", Intent: "x"})
	if err != nil {
		t.Fatalf("summary pack: %v", err)
	}
	if hash != "" || level != "summary" {
		t.Fatalf("summary pack = (%q,%q), want (\"\",summary)", hash, level)
	}

	hash, level, err = c.packContext(ctx, TaskInput{ContextType: "file", RepoPath: "/repo/panda"})
	if err != nil {
		t.Fatalf("file pack: %v", err)
	}
	if hash == "" || level != "pointer" {
		t.Fatalf("file pack = (%q,%q), want (non-empty,pointer)", hash, level)
	}
	if ok, _ := c.ctx.Contains(ctx, hash); !ok {
		t.Fatalf("file pack did not cache the snapshot")
	}

	hash, level, err = c.packContext(ctx, TaskInput{ContextHash: "abc123", ContextLevel: "full"})
	if err != nil {
		t.Fatalf("passthrough pack: %v", err)
	}
	if hash != "abc123" || level != "full" {
		t.Fatalf("passthrough pack = (%q,%q), want (abc123,full)", hash, level)
	}
}

// TestContextPointerHit verifies a pointer delegation whose snapshot is already
// cached on the executor runs with zero transfer: the leaf never enters
// waiting_context and the result still returns to the root.
func TestContextPointerHit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	root := newCore(t, "root-hit", "127.0.0.1:17941")
	leaf := newCoreWithNative(t, "leaf-hit", "127.0.0.1:17942", ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"gpio-ok"},
	})
	startPair(t, ctx, root, leaf, "127.0.0.1:17941", "127.0.0.1:17942")

	// Pre-pack a snapshot and seed it on the leaf, so the pointer resolves
	// locally without a context_fetch round-trip.
	snap := ctxstore.Snapshot{Type: "file", Data: json.RawMessage(`{"repo":"/r","branch":"main"}`)}
	hash, blob, err := ctxstore.Pack(snap)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := leaf.ctx.Put(ctx, hash, "file", blob, nil); err != nil {
		t.Fatalf("seed leaf: %v", err)
	}

	in := TaskInput{
		Title: "pointer hit", ContextType: "file",
		ContextHash: hash, ContextLevel: "pointer",
		Intent: "read gpio", Requires: []string{"gpio:read"},
	}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK || !strings.Contains(result.Stdout, "gpio-ok") {
		t.Fatalf("result = %+v, want gpio-ok", result)
	}
	if task.State != StateDone {
		t.Fatalf("root task state = %s, want done", task.State)
	}

	// A pointer hit must not park the task in waiting_context.
	evs, _ := leaf.store.Events(ctx, task.TaskID)
	for _, e := range evs {
		if e.Type == EvProgress && strings.Contains(e.DataJSON, "waiting") {
			t.Fatalf("leaf entered waiting_context on a pointer hit: %+v", evs)
		}
	}
}

// TestContextFetchMiss exercises the full context-transfer loop: the root packs
// and stores a file snapshot, forwards a pointer, the leaf misses and fetches
// it from the root, verifies the SHA-256, resumes, executes, and relays the
// result back.
func TestContextFetchMiss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	root := newCore(t, "root-miss", "127.0.0.1:17951")
	leaf := newCoreWithNative(t, "leaf-miss", "127.0.0.1:17952", ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"gpio-ok"},
	})
	startPair(t, ctx, root, leaf, "127.0.0.1:17951", "127.0.0.1:17952")

	in := TaskInput{
		Title: "fetch miss", ContextType: "file", RepoPath: "/repo/panda",
		Intent: "read gpio", Requires: []string{"gpio:read"},
	}
	task, result, err := root.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK || !strings.Contains(result.Stdout, "gpio-ok") {
		t.Fatalf("result = %+v, want gpio-ok", result)
	}
	if task.State != StateDone {
		t.Fatalf("root task state = %s, want done", task.State)
	}
	if task.ContextHash == "" {
		t.Fatalf("root task context_hash empty")
	}

	// The leaf must have completed and recorded the same context hash.
	lt, err := leaf.store.Get(ctx, task.TaskID)
	if err != nil || lt.State != StateDone {
		t.Fatalf("leaf task = %+v (err %v), want done", lt, err)
	}
	if lt.ContextHash != task.ContextHash {
		t.Fatalf("leaf context_hash = %q, want %q", lt.ContextHash, task.ContextHash)
	}
	// The fetched snapshot must now be cached locally.
	if ok, _ := leaf.ctx.Contains(ctx, task.ContextHash); !ok {
		t.Fatalf("leaf did not cache the fetched snapshot")
	}

	// The leaf's timeline must show the waiting_context → resume sequence.
	evs, _ := leaf.store.Events(ctx, task.TaskID)
	sawWait, sawResume := false, false
	for _, e := range evs {
		if e.Type == EvProgress && strings.Contains(e.DataJSON, "waiting") {
			sawWait = true
		}
		if e.Type == EvProgress && strings.Contains(e.DataJSON, "resumed") {
			sawResume = true
		}
	}
	if !sawWait || !sawResume {
		t.Fatalf("leaf events missing wait/resume: wait=%v resume=%v (%+v)", sawWait, sawResume, evs)
	}
}
