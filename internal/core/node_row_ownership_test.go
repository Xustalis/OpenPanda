// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestBorrowedEngineShutdownLeavesRowOnline pins the row-ownership contract:
// a Core that only borrows the node identity — a CLI engine sharing the
// daemon's stable id — must not mark the directory row offline at Shutdown.
// Before the ownsNodeRow flag, `panda task add` exiting flipped the live
// daemon's self row to offline until the next heartbeat, and a routing pass
// in that window saw an empty local candidate set.
func TestBorrowedEngineShutdownLeavesRowOnline(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{Device: "dev", Native: []ledger.NativeAbility{{ID: "sys:info"}}}
	if err := ledger.Register(db, card, "node-x", 5); err != nil {
		t.Fatalf("register: %v", err)
	}

	c := NewCore(db, "node-x", card, 5, testLogger(), config.ModelConfig{})
	c.SetOwnsNodeRow(false)
	c.Shutdown(ctx)

	nodes, err := ledger.Query(db, "online", "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-x" {
		t.Fatalf("borrowed shutdown marked the row offline: %+v", nodes)
	}

	// The owning daemon still retires its row — the flag must not leak the
	// other direction and strand a dead node online forever.
	owner := NewCore(db, "node-x", card, 5, testLogger(), config.ModelConfig{})
	owner.Shutdown(ctx)
	nodes, err = ledger.Query(db, "online", "")
	if err != nil {
		t.Fatalf("query2: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("owning shutdown left the row online: %+v", nodes)
	}
}

// TestExtractSkipSetProtectsLiveState pins the adopt-side half of the host
// state contract: a peer's project tree must never overwrite this node's
// live SQLite file — replacing it orphans the daemon's open handle on a dead
// inode while every later reader sees the imported snapshot.
func TestExtractSkipSetProtectsLiveState(t *testing.T) {
	dst := t.TempDir()
	c := NewCore(openTestDB(t), "node-y", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetHostStatePaths([]string{
		filepath.Join(dst, "data"),            // sqlite dir inside the destination
		filepath.Join(dst, "data", "node.db"), // file-level entry
		filepath.Join(dst, "data", "node.db-wal"),
		"/outside/elsewhere", // outside dst: must not appear in the set
	})

	skip := c.extractSkipSet(dst)
	for _, want := range []string{"data", "data/node.db", "data/node.db-wal"} {
		if !skip[want] {
			t.Fatalf("extract skip set missing %q: %v", want, skip)
		}
	}
	// Static protections survive alongside the host entries.
	if !skip[".git"] {
		t.Fatal("extract skip set lost the static protected paths")
	}

	// No state configured → the bare static set, untouched.
	bare := NewCore(openTestDB(t), "node-z", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	if got := bare.extractSkipSet(dst); len(got) != len(extractProtectedDirs) {
		t.Fatalf("no host state should return the static set, got %v", got)
	}
}

// TestHostStatePruneInsideRootOnly pins the pack-side mapping: state inside
// the packed tree prunes; state elsewhere never matches; a state path equal
// to the root is dropped rather than emptying the artifact.
func TestHostStatePruneInsideRootOnly(t *testing.T) {
	root := t.TempDir()
	c := NewCore(openTestDB(t), "node-p", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	inside := filepath.Join(root, "data", "node.db")
	c.SetHostStatePaths([]string{inside, "/elsewhere/x.db", root})
	got := c.hostStatePrune(root)
	if len(got) != 1 || got[0] != inside {
		t.Fatalf("prune = %v, want just %q", got, inside)
	}
}
