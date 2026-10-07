// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
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
//
// Entries can carry the node's Ed25519 attestation (P2-9): the signature
// covers the entry's chain hash, so a row cannot be rewritten and re-hashed
// by a DB writer who lacks the private key. Signing is installed with
// SetSigner; a key-less Audit records the historical unsigned shape.
type Audit struct {
	db *sql.DB
	// keyMu guards pub/priv: SetSigner runs at wiring time, Record and
	// VerifyChain read from serving goroutines.
	keyMu sync.RWMutex
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
}

// NewAudit wraps a DB.
func NewAudit(db *sql.DB) *Audit { return &Audit{db: db} }

// SetSigner installs the node identity used to sign recorded entries and to
// bind verification to this node's key. priv may be nil for callers that
// only verify (`panda audit verify`, the panel's integrity endpoint): with
// no private key nothing is signed, but signatures that do exist must be by
// pub.
func (a *Audit) SetSigner(pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	a.keyMu.Lock()
	a.pub, a.priv = pub, priv
	a.keyMu.Unlock()
}

// Record writes one entry, linking it to the global audit hash chain (A3).
// The read of the chain head and the insert must live in one transaction:
// two concurrent Records that each read the same head would both link to it,
// forking the chain and failing VerifyChain on one of them (M2). The store's
// single connection serializes the tx bodies, so the second writer always sees
// the first writer's row as the head.
func (a *Audit) Record(ctx context.Context, e Entry) error {
	ts := time.Now().Unix()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prevHash, err := lastHash(ctx, tx)
	if err != nil {
		return fmt.Errorf("read prev audit hash: %w", err)
	}
	// P2-9: sign the row's own chain hash — the commitment to every field
	// plus its position — so the signature covers content AND place in one
	// primitive. Pure-CPU: the key is resolved at wiring time, and a missing
	// key just leaves the columns empty (legacy shape).
	var sig, sigPub string
	a.keyMu.RLock()
	if a.priv != nil {
		sig = hex.EncodeToString(ed25519.Sign(a.priv, []byte(hashAudit(prevHash, ts, e.Who, e.What, e.Target, e.Result, e.Detail))))
		sigPub = hex.EncodeToString(a.pub)
	}
	a.keyMu.RUnlock()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash, sig, sig_pub)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, e.Who, e.What, e.Target, e.Result, e.Detail, prevHash, sig, sigPub); err != nil {
		return err
	}
	return tx.Commit()
}

// lastHash returns the hash of the most recent audit_log entry, or "" for the
// genesis entry. It runs inside the caller's transaction so the head it reads
// is the head the insert links to.
func lastHash(ctx context.Context, tx *sql.Tx) (string, error) {
	var prev struct {
		PrevHash string
		TS       int64
		Who      string
		What     string
		Target   string
		Result   string
		Detail   string
	}
	err := tx.QueryRowContext(ctx,
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
	// Sig/SigPub are the P2-9 attestation: the Ed25519 signature over this
	// row's chain hash and the public key that produced it (both hex). Empty
	// on rows recorded before entry signing or by an Audit without a key.
	Sig    string
	SigPub string
}

// Entries returns all audit rows, oldest first.
func (a *Audit) Entries(ctx context.Context) ([]AuditRow, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, ts, who, what, target, result, detail, COALESCE(prev_hash, ''), COALESCE(sig, ''), COALESCE(sig_pub, '') FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.TS, &r.Who, &r.What, &r.Target, &r.Result, &r.Detail, &r.PrevHash, &r.Sig, &r.SigPub); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerifyChain verifies the global audit hash chain. It returns nil if intact,
// or an error describing the first break.
//
// The signature rules mirror TaskStore.VerifyTaskEventChain (P2-9
// hardening): when the Audit knows the node's key, a signature must be BY
// THAT KEY (verifying against whatever key a row names would let a DB writer
// re-sign the chain under a keypair of their own), and once a signed row
// exists no later row may be unsigned (tail stripping). Pre-signing rows
// form a legacy unsigned prefix — there is nothing to check them against.
func (a *Audit) VerifyChain(ctx context.Context) error {
	rows, err := a.Entries(ctx)
	if err != nil {
		return fmt.Errorf("load audit rows: %w", err)
	}
	a.keyMu.RLock()
	expected := ""
	if a.pub != nil {
		expected = hex.EncodeToString(a.pub)
	}
	a.keyMu.RUnlock()
	var prevHash string
	signedSeen := false
	for i, r := range rows {
		if r.PrevHash != prevHash {
			return fmt.Errorf("audit row %d (id=%d) prev_hash mismatch: got %s, want %s",
				i+1, r.ID, r.PrevHash, prevHash)
		}
		h := hashAudit(r.PrevHash, r.TS, r.Who, r.What, r.Target, r.Result, r.Detail)
		if r.Sig == "" {
			if signedSeen {
				return fmt.Errorf("audit row %d (id=%d) unsigned after signed rows — signature stripped", i+1, r.ID)
			}
		} else {
			if expected != "" && r.SigPub != expected {
				return fmt.Errorf("audit row %d (id=%d) signed by key %s, want this node's key %s",
					i+1, r.ID, r.SigPub, expected)
			}
			if err := verifyAuditSig(r.SigPub, h, r.Sig); err != nil {
				return fmt.Errorf("audit row %d (id=%d) signature invalid: %w", i+1, r.ID, err)
			}
			signedSeen = true
		}
		prevHash = h
	}
	return nil
}

// verifyAuditSig checks a row's signature against the recomputed chain hash
// and the public key the row names. A torn signature block (sig without
// pub, undecodable hex) fails closed — a field can only go missing by
// deletion, which is exactly what verification exists to expose.
func verifyAuditSig(sigPub, chainHash, sig string) error {
	pub, err := hex.DecodeString(sigPub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad sig_pub")
	}
	raw, err := hex.DecodeString(sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("bad sig encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(chainHash), raw) {
		return errors.New("ed25519 verify failed")
	}
	return nil
}
