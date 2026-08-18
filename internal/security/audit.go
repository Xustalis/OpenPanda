package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Entry is one high-risk operation record (plan P3-32). High-risk means any
// Tier-2 execution or denial, any circuit trip, and any adapter spawn — the
// operations whose "who / what / result" must be reconstructable later.
type Entry struct {
	Who    string // actor node id
	What   string // operation, e.g. "native:tier2", "circuit:open", "agent:spawn"
	Target string // task id / agent / command
	Result string // "authorized" / "denied" / "ok" / "failed" / "open"
	Detail string // extra context, never secrets
}

// Audit appends high-risk operation records to the audit_log table. Callers
// pass a DB whose schema includes audit_log (see storage.Migrate).
type Audit struct{ db *sql.DB }

// NewAudit wraps a DB.
func NewAudit(db *sql.DB) *Audit { return &Audit{db: db} }

// Record writes one entry, linking it to the global audit hash chain (A3).
func (a *Audit) Record(ctx context.Context, e Entry) error {
	ts := time.Now().Unix()
	prevHash, err := a.lastHash(ctx)
	if err != nil {
		return fmt.Errorf("read prev audit hash: %w", err)
	}
	_, err = a.db.ExecContext(ctx, `
		INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, e.Who, e.What, e.Target, e.Result, e.Detail, prevHash)
	return err
}

// lastHash returns the hash of the most recent audit_log entry, or "" for the
// genesis entry.
func (a *Audit) lastHash(ctx context.Context) (string, error) {
	var prev struct {
		PrevHash string
		TS       int64
		Who      string
		What     string
		Target   string
		Result   string
		Detail   string
	}
	err := a.db.QueryRowContext(ctx,
		`SELECT COALESCE(prev_hash, ''), ts, who, what, target, result, detail FROM audit_log
		 ORDER BY id DESC LIMIT 1`).Scan(
		&prev.PrevHash, &prev.TS, &prev.Who, &prev.What, &prev.Target, &prev.Result, &prev.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hashAudit(prev.PrevHash, prev.TS, prev.Who, prev.What, prev.Target, prev.Result, prev.Detail), nil
}

func hashAudit(prevHash string, ts int64, who, what, target, result, detail string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s|%s", prevHash, ts, who, what, target, result, detail)
	return hex.EncodeToString(h.Sum(nil))
}

// AuditRow is one audit_log row.
type AuditRow struct {
	ID       int64
	TS       int64
	Who      string
	What     string
	Target   string
	Result   string
	Detail   string
	PrevHash string
}

// Entries returns all audit rows, oldest first.
func (a *Audit) Entries(ctx context.Context) ([]AuditRow, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, ts, who, what, target, result, detail, COALESCE(prev_hash, '') FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.TS, &r.Who, &r.What, &r.Target, &r.Result, &r.Detail, &r.PrevHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerifyChain verifies the global audit hash chain. It returns nil if intact,
// or an error describing the first break.
func (a *Audit) VerifyChain(ctx context.Context) error {
	rows, err := a.Entries(ctx)
	if err != nil {
		return fmt.Errorf("load audit rows: %w", err)
	}
	var prevHash string
	for i, r := range rows {
		if r.PrevHash != prevHash {
			return fmt.Errorf("audit row %d (id=%d) prev_hash mismatch: got %s, want %s",
				i+1, r.ID, r.PrevHash, prevHash)
		}
		prevHash = hashAudit(r.PrevHash, r.TS, r.Who, r.What, r.Target, r.Result, r.Detail)
	}
	return nil
}
