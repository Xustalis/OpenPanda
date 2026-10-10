// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// TestElidePathKeepsTail pins the workspace column's shortening: the tail —
// the part that names the directory — must survive, with a marker showing the
// cut, and a path that fits passes through untouched.
func TestElidePathKeepsTail(t *testing.T) {
	if got := elidePath("/Users/x/work/project", 30); got != "/Users/x/work/project" {
		t.Fatalf("fitting path changed: %q", got)
	}
	got := elidePath("/Users/xenith/Library/Application Support/openpanda/stages/plan-x/stage-1", 30)
	if len([]rune(got)) > 30 {
		t.Fatalf("elided path longer than cap: %q", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("elided path lacks the cut marker: %q", got)
	}
	if !strings.HasSuffix(got, "stage-1") {
		t.Fatalf("elided path lost the tail: %q", got)
	}
	if got := elidePath("", 30); got != "" {
		t.Fatalf("empty path = %q, want empty", got)
	}
}

// TestMeshLineCountsConfiguredPeersOnly pins the denominator fix: the mesh
// summary must count online CONFIGURED peers, not live directory rows. The
// old numerator let an inbound-only node (reachable, never configured) make a
// dead configured peer read as "1/1 online".
func TestMeshLineCountsConfiguredPeersOnly(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	self := ledger.Node{ID: "self-node", Status: "online", Neighbors: []string{"node-b"}}
	inbound := ledger.Node{ID: "node-c", Status: "online"}
	views := []nodeStatusView{
		{Node: self, Local: true, Running: true},
		{Node: inbound, Local: false, Running: true},
	}
	cfg := &config.Config{}
	cfg.Network.Peers = []string{"10.0.0.9:7836"}
	cfg.Network.ListenAddr = "127.0.0.1:1" // refused instantly; the listener state is not under test

	line := func() string {
		var buf bytes.Buffer
		printMeshLineTo(&buf, i18n.Locale("en"), cfg, db, views)
		return buf.String()
	}

	// No binding for the configured peer: the live inbound node must NOT
	// count — 0 of 1 configured peers are up.
	if got := line(); !strings.Contains(got, "0/1") {
		t.Fatalf("mesh line = %q, want 0/1 (inbound-only node must not count)", got)
	}

	// Bind the configured address to node-b and make node-b live: 1 of 1.
	if err := ledger.RecordPeerAddr(db, "10.0.0.9:7836", "node-b"); err != nil {
		t.Fatalf("record binding: %v", err)
	}
	if got := line(); !strings.Contains(got, "1/1") {
		t.Fatalf("mesh line = %q, want 1/1 after the configured peer binds and is live", got)
	}

	// A binding for a NON-configured node (node-c) must not inflate the
	// numerator: still 1 of 1.
	if err := ledger.RecordPeerAddr(db, "10.0.0.10:7836", "node-c"); err != nil {
		t.Fatalf("record binding: %v", err)
	}
	if got := line(); !strings.Contains(got, "1/1") {
		t.Fatalf("mesh line = %q, want still 1/1 (unconfigured binding must not count)", got)
	}

	// The configured peer goes offline (no longer a live neighbor): 0 of 1
	// even though the inbound node remains live.
	views[0].Neighbors = nil
	if got := line(); !strings.Contains(got, "0/1") {
		t.Fatalf("mesh line = %q, want 0/1 after the configured peer dropped", got)
	}
}
