package ledger

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xenith/panda/internal/storage"
)

// writeCard serializes a Card to YAML at path (mirrors LoadCard).
func writeCard(t *testing.T, path string, c Card) {
	t.Helper()
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write card: %v", err)
	}
}

func openLedgerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func testCard() Card {
	return Card{
		Device:        "opi3b",
		ResourceClass: "Micro",
		Chip:          "RK3566",
		Native: []NativeAbility{
			{ID: "sys:info", Command: "uname"},
		},
		Manual: []ManualAbility{
			{ID: "design:figma", Notify: "open figma"},
		},
		Capacity: Capacity{CPUCores: 4, RAMGB: 2, MaxConcurrent: 1},
	}
}

func TestRegisterQueryLifecycle(t *testing.T) {
	db := openLedgerDB(t)
	c := testCard()

	if err := Register(db, c, "opi3b", 1); err != nil {
		t.Fatalf("register: %v", err)
	}

	nodes, err := Query(db, "online", "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "opi3b" {
		t.Fatalf("expected 1 online node, got %+v", nodes)
	}
	if len(nodes[0].Native) != 1 || nodes[0].Native[0].ID != "sys:info" {
		t.Fatalf("native not round-tripped: %+v", nodes[0].Native)
	}
	if nodes[0].Capacity.RAMGB != 2 {
		t.Fatalf("capacity not round-tripped: %+v", nodes[0].Capacity)
	}
}

func TestHeartbeatUpdatesStatus(t *testing.T) {
	db := openLedgerDB(t)
	c := testCard()
	if err := Register(db, c, "opi3b", 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := Heartbeat(db, "opi3b", "busy", "{}"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	nodes, _ := Query(db, "busy", "")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 busy node, got %d", len(nodes))
	}
	// Filtering by old status yields nothing.
	online, _ := Query(db, "online", "")
	if len(online) != 0 {
		t.Fatalf("expected 0 online, got %d", len(online))
	}
}

func TestMarkOffline(t *testing.T) {
	db := openLedgerDB(t)
	c := testCard()
	if err := Register(db, c, "opi3b", 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := MarkOffline(db, "opi3b"); err != nil {
		t.Fatalf("offline: %v", err)
	}
	offline, _ := Query(db, "offline", "")
	if len(offline) != 1 {
		t.Fatalf("expected 1 offline node, got %d", len(offline))
	}
}

func TestQueryByNameFilter(t *testing.T) {
	db := openLedgerDB(t)
	if err := Register(db, testCard(), "a", 1); err != nil {
		t.Fatalf("register a: %v", err)
	}
	c := testCard()
	c.Device = "b"
	if err := Register(db, c, "b", 1); err != nil {
		t.Fatalf("register b: %v", err)
	}

	nodes, err := Query(db, "", "b")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "b" {
		t.Fatalf("name filter failed: %+v", nodes)
	}
}

func TestLoadCardFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "capabilities.yaml")
	writeCard(t, p, testCard())
	card, err := LoadCard(p)
	if err != nil {
		t.Fatalf("load card: %v", err)
	}
	if card.Device != "opi3b" || len(card.Native) != 1 {
		t.Fatalf("card = %+v", card)
	}
}

func TestLoadCardMissingFileErrors(t *testing.T) {
	if _, err := LoadCard(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatalf("expected error for missing card")
	}
}
