package core

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xenith/openpanda/internal/ledger"
)

func newTestNode(t *testing.T) *Node {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "test-node",
		ResourceClass: "Micro",
		Chip:          "test",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "uname", Args: []string{"-a"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 2, MaxConcurrent: 1},
	}
	logger := slog.New(slog.DiscardHandler)
	return NewNode(db, "test-node", card, 1, logger)
}

func TestEphemeralNodeIDUniqueAndPrefixed(t *testing.T) {
	a := EphemeralNodeID("macbook")
	b := EphemeralNodeID("macbook")
	if a == b {
		t.Fatalf("two ephemeral ids must differ, got %q twice", a)
	}
	if a == "macbook" || b == "macbook" {
		t.Fatalf("ephemeral id must not equal the bare base name")
	}
	if !strings.HasPrefix(a, "macbook-") || !strings.HasPrefix(b, "macbook-") {
		t.Fatalf("ephemeral id must be prefixed by base: %q %q", a, b)
	}
}

func TestRegisterAndQuery(t *testing.T) {
	n := newTestNode(t)
	ctx := context.Background()
	if err := n.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	nodes, err := n.List("online", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].ID != "test-node" {
		t.Fatalf("expected id test-node, got %s", nodes[0].ID)
	}
	if len(nodes[0].Native) != 1 || nodes[0].Native[0].ID != "sys:info" {
		t.Fatalf("expected native ability sys:info, got %+v", nodes[0].Native)
	}
}

func TestHeartbeatUpdatesLastSeen(t *testing.T) {
	n := newTestNode(t)
	ctx := context.Background()
	if err := n.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	before, _ := n.List("online", "")
	time.Sleep(20 * time.Millisecond)
	n.beat(ctx)
	after, _ := n.List("online", "")
	if !(after[0].LastSeen >= before[0].LastSeen) {
		t.Fatalf("last_seen did not advance: %d -> %d", before[0].LastSeen, after[0].LastSeen)
	}
}

func TestShutdownMarksOffline(t *testing.T) {
	n := newTestNode(t)
	ctx := context.Background()
	if err := n.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	n.Shutdown(ctx)

	nodes, err := n.List("offline", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 offline node, got %d", len(nodes))
	}
}
