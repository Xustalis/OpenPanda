package security

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func openAuditTestDB(t *testing.T) *sql.DB {
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

func TestAuditRecordPersists(t *testing.T) {
	db := openAuditTestDB(t)

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

// TestAuditChainValid verifies the global audit hash chain is intact after
// recording several entries.
func TestAuditChainValid(t *testing.T) {
	db := openAuditTestDB(t)
	a := NewAudit(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := a.Record(ctx, Entry{
			Who:    fmt.Sprintf("node-%d", i),
			What:   "native:tier2",
			Target: fmt.Sprintf("task-%d", i),
			Result: "authorized",
			Detail: "ok",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	if err := a.VerifyChain(ctx); err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
}

// TestAuditChainTamperDetect verifies VerifyChain detects a mutated audit row.
func TestAuditChainTamperDetect(t *testing.T) {
	db := openAuditTestDB(t)
	a := NewAudit(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := a.Record(ctx, Entry{
			Who:    fmt.Sprintf("node-%d", i),
			What:   "native:tier2",
			Target: fmt.Sprintf("task-%d", i),
			Result: "authorized",
			Detail: "ok",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	res, err := db.ExecContext(ctx,
		`UPDATE audit_log SET result=? WHERE id=?`,
		"tampered", 2)
	if err != nil {
		t.Fatalf("tamper audit row: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected to tamper 1 row, got %d", n)
	}

	if err := a.VerifyChain(ctx); err == nil {
		t.Fatalf("expected tamper detection error, got nil")
	}
}

func TestAuditRecordConcurrentNoFork(t *testing.T) {
	db := openAuditTestDB(t)
	a := NewAudit(db)
	ctx := context.Background()

	const workers = 10
	const perWorker = 5
	var wg sync.WaitGroup
	errCh := make(chan error, workers*perWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				err := a.Record(ctx, Entry{
					Who:    fmt.Sprintf("node-%d", workerID),
					What:   "native:tier2",
					Target: fmt.Sprintf("task-%d-%d", workerID, i),
					Result: "authorized",
					Detail: "ok",
				})
				if err != nil {
					errCh <- fmt.Errorf("worker %d item %d: %w", workerID, i, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent record failed: %v", err)
	}

	if err := a.VerifyChain(ctx); err != nil {
		t.Fatalf("verify chain after concurrent records: %v", err)
	}
}

