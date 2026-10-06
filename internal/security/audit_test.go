package security

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
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

// TestAuditSigning (P2-9): an Audit with the node key signs every entry; the
// rows verify under the recorded key, including from a verify-only caller
// that knows the public half.
func TestAuditSigning(t *testing.T) {
	db := openAuditTestDB(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	a := NewAudit(db)
	a.SetSigner(pub, priv)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := a.Record(ctx, Entry{
			Who: "node-a", What: "native:tier2",
			Target: fmt.Sprintf("task-%d", i), Result: "authorized", Detail: "ok",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if err := a.VerifyChain(ctx); err != nil {
		t.Fatalf("verify signed chain: %v", err)
	}
	rows, err := a.Entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	for _, r := range rows {
		if r.Sig == "" || r.SigPub != hex.EncodeToString(pub) {
			t.Fatalf("row %d sig=%q sig_pub=%q, want this node's key", r.ID, r.Sig, r.SigPub)
		}
	}

	verifier := NewAudit(db)
	verifier.SetSigner(pub, nil)
	if err := verifier.VerifyChain(ctx); err != nil {
		t.Fatalf("verify-only binding rejected its own chain: %v", err)
	}
}

// TestAuditForeignReSignRejected (P2-9 hardening): a DB writer who rewrites
// entries AND re-signs the whole chain with a keypair of their own still
// fails verification, because the Audit knows this node's key.
func TestAuditForeignReSignRejected(t *testing.T) {
	db := openAuditTestDB(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	a := NewAudit(db)
	a.SetSigner(pub, priv)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := a.Record(ctx, Entry{
			Who: "node-a", What: "native:tier2",
			Target: fmt.Sprintf("task-%d", i), Result: "denied", Detail: "rm -rf /",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	attackerPub, attackerPriv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("attacker key: %v", err)
	}
	rows, err := a.Entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	prevHash := ""
	for _, r := range rows {
		forged := "rewritten"
		h := hashAudit(prevHash, r.TS, r.Who, r.What, r.Target, r.Result, forged)
		sig := hex.EncodeToString(ed25519.Sign(attackerPriv, []byte(h)))
		if _, err := db.Exec(
			`UPDATE audit_log SET detail=?, prev_hash=?, sig=?, sig_pub=? WHERE id=?`,
			forged, prevHash, sig, hex.EncodeToString(attackerPub), r.ID); err != nil {
			t.Fatalf("re-sign row %d: %v", r.ID, err)
		}
		prevHash = h
	}

	if err := a.VerifyChain(ctx); err == nil {
		t.Fatal("a chain re-signed under a foreign key passed verification")
	}
}

// TestAuditSignatureStrippingRejected (P2-9 hardening): deleting the
// signature columns from the tail of a signed chain is detected — the
// unsigned prefix may only precede the first signed row.
func TestAuditSignatureStrippingRejected(t *testing.T) {
	db := openAuditTestDB(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	a := NewAudit(db)
	a.SetSigner(pub, priv)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := a.Record(ctx, Entry{
			Who: "node-a", What: "native:tier2",
			Target: fmt.Sprintf("task-%d", i), Result: "authorized", Detail: "ok",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	if _, err := db.Exec(`UPDATE audit_log SET sig='', sig_pub='' WHERE id=(SELECT MAX(id) FROM audit_log)`); err != nil {
		t.Fatalf("strip tail signature: %v", err)
	}
	if err := a.VerifyChain(ctx); err == nil {
		t.Fatal("a stripped tail signature passed verification")
	}
}
