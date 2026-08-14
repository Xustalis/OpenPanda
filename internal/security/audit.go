package security

import (
	"context"
	"database/sql"
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

// Record writes one entry. It logs a failure to the caller rather than
// returning it, because audit must never break the hot execution path.
func (a *Audit) Record(ctx context.Context, e Entry) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO audit_log (ts, who, what, target, result, detail)
		VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Who, e.What, e.Target, e.Result, e.Detail)
	return err
}
