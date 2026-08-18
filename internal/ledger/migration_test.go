package ledger

import (
	"testing"

	"github.com/xenith/openpanda/internal/storage"
)

// TestQuerySurvivesLegacyNullResourceProfile reproduces the upgrade path where a
// database predates the resource_profile_json column: addColumnIfMissing adds the
// column as NULL for existing rows, and Query must still return them rather than
// fail the whole directory scan on a NULL-to-string conversion.
func TestQuerySurvivesLegacyNullResourceProfile(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	legacy := `CREATE TABLE employee_cache (
		id TEXT PRIMARY KEY, name TEXT, department TEXT, chip TEXT,
		native_json TEXT, agents_json TEXT, manual_json TEXT, capacity_json TEXT,
		status TEXT, last_seen INTEGER, scheduler_tier INTEGER
	)`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO employee_cache
		(id, name, chip, status, last_seen, scheduler_tier)
		VALUES ('legacy', 'n', 'c', 'offline', 0, 1)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate legacy db: %v", err)
	}

	nodes, err := Query(db, "", "")
	if err != nil {
		t.Fatalf("query after legacy migration: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(nodes))
	}
}
