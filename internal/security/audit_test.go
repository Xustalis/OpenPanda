package security

import (
	"context"
	"testing"

	"github.com/xenith/panda/internal/storage"
)

func TestAuditRecordPersists(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	a := NewAudit(db)
	if err := a.Record(context.Background(), Entry{
		Who:    "node-a",
		What:   "native:tier2",
		Target: "task-1",
		Result: "denied",
		Detail: "rm -rf /",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var who, what, result, detail string
	if err := db.QueryRow(`SELECT who, what, result, detail FROM audit_log LIMIT 1`).
		Scan(&who, &what, &result, &detail); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if who != "node-a" || what != "native:tier2" || result != "denied" || detail != "rm -rf /" {
		t.Fatalf("unexpected row: who=%q what=%q result=%q detail=%q", who, what, result, detail)
	}
}
