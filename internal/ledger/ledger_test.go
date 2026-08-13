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

func TestUpsertRemoteRoundTrip(t *testing.T) {
	db := openLedgerDB(t)
	sum := CapabilitySummary{
		Device:        "windows",
		ResourceClass: "Standard",
		SchedulerTier: 5,
		Chip:          "x86_64",
		NativeIDs:     []string{"gpio:read"},
		AgentCaps:     map[string][]string{"claude_code": {"code:modify"}},
		ManualIDs:     []string{"design:figma"},
		Capacity:      Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	if err := UpsertRemote(db, "windows", sum); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	nodes, err := Query(db, "online", "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 online node, got %d", len(nodes))
	}
	n := nodes[0]
	if n.ID != "windows" || n.SchedulerTier != 5 || n.Chip != "x86_64" {
		t.Fatalf("node fields not round-tripped: %+v", n)
	}
	// Remote abilities are stored ID-only (no executable commands), but the
	// IDs must survive for routing.
	if len(n.Native) != 1 || n.Native[0].ID != "gpio:read" {
		t.Fatalf("native ids not round-tripped: %+v", n.Native)
	}
	if cap, ok := n.Agents["claude_code"]; !ok || len(cap.Capabilities) != 1 || cap.Capabilities[0] != "code:modify" {
		t.Fatalf("agent caps not round-tripped: %+v", n.Agents)
	}
	if len(n.Manual) != 1 || n.Manual[0].ID != "design:figma" {
		t.Fatalf("manual ids not round-tripped: %+v", n.Manual)
	}

	// Upserting again must update in place, not duplicate.
	sum2 := sum
	sum2.Capacity = Capacity{CPUCores: 16, RAMGB: 32, MaxConcurrent: 5}
	if err := UpsertRemote(db, "windows", sum2); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	nodes, _ = Query(db, "online", "")
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after re-upsert, got %d", len(nodes))
	}
}

func TestNodeMatches(t *testing.T) {
	n := Node{
		Native: []NativeAbility{{ID: "gpio:read"}},
		Agents: map[string]Agent{"claude_code": {Capabilities: []string{"code:modify"}}},
		Manual: []ManualAbility{{ID: "design:figma"}},
	}
	for _, req := range []string{"gpio:read", "code:modify", "design:figma"} {
		if !n.Matches([]string{req}) {
			t.Fatalf("Matches(%q) = false, want true across all three layers", req)
		}
	}
	if n.Matches([]string{"gpio:write", "unknown"}) {
		t.Fatalf("Matches should be false for undeclared abilities")
	}
	if n.Matches([]string{}) {
		t.Fatalf("Matches(empty) should be false")
	}
}

func TestNodeMatchesNormalized(t *testing.T) {
	n := Node{
		Native: []NativeAbility{{ID: "lint"}},
		Agents: map[string]Agent{"claude_code": {Capabilities: []string{"code:modify"}}},
	}
	// "code:lint" bridges to the card id "lint" via normalized containment.
	if !n.Matches([]string{"code:lint"}) {
		t.Fatalf("Matches(code:lint) should bridge to lint")
	}
	// "agent:claude_code" refers to the agent by name (as the device summary
	// advertises it).
	if !n.Matches([]string{"agent:claude_code"}) {
		t.Fatalf("Matches(agent:claude_code) should match by name")
	}
	// A degenerate 2-char fragment must not fan out to unrelated abilities.
	if n.Matches([]string{"li"}) {
		t.Fatalf("Matches(li) should be false for a 2-char fragment")
	}
}

func TestAbilityMatches(t *testing.T) {
	cases := []struct {
		declared, required string
		want               bool
	}{
		{"lint", "lint", true},               // exact
		{"lint", "code:lint", true},          // category prefix
		{"build:macos", "build", true},       // suffix
		{"build:macos", "BUILD_MACOS", true}, // case + separator fold
		{"lint", "gpu:train", false},         // unrelated
		{"io", "gpio:write", false},          // degenerate fragment guarded
		{"", "lint", false},                  // empty declared
		{"lint", "", false},                  // empty required
	}
	for _, tc := range cases {
		if got := AbilityMatches(tc.declared, tc.required); got != tc.want {
			t.Fatalf("AbilityMatches(%q, %q) = %v, want %v", tc.declared, tc.required, got, tc.want)
		}
	}
}
