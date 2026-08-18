// Package ctxstore implements the local context snapshot cache (design doc
// §12.4): a hash→data KV store over the `context` table with LRU eviction.
// Each node caches full context snapshots it has either packed locally or
// fetched from a peer, keyed by SHA-256, so a repeated pointer delegation can
// hit the cache and transfer nothing.
package ctxstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xenith/openpanda/internal/storage"
)

// Entry is one stored context snapshot.
type Entry struct {
	Hash string
	Type string
	Data []byte // canonical serialization of the full snapshot
	Refs []string
}

// Store is a cache over the context table.
type Store struct {
	db  *sql.DB
	max int // LRU cap; 0 means unlimited
}

// New wraps a DB. max is the entry cap; a non-positive max disables eviction.
func New(db *sql.DB, max int) *Store {
	return &Store{db: db, max: max}
}

// MaxEntriesForResourceClass maps a node resource class to its context-store
// cap (design doc §12.4): Micro keeps few snapshots on constrained storage,
// Standard/Full keep more, and a Full node is effectively unbounded.
func MaxEntriesForResourceClass(rc string) int {
	switch rc {
	case "Micro":
		return 5
	case "Full":
		return 0
	default:
		return 50 // Standard and any unknown class
	}
}

// Put upserts a snapshot under its hash and evicts least-recently-accessed
// entries once the store exceeds its cap. The upsert and the count→select→
// delete eviction run in one transaction (P2-14): outside a tx, concurrent
// Puts can each read an over-cap count and each evict — or a crash between
// upsert and evict leaves the store over its cap indefinitely.
func (s *Store) Put(ctx context.Context, hash, typ string, data []byte, refs []string) error {
	refsJSON, _ := json.Marshal(refs)
	now := storage.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	_, err = tx.ExecContext(ctx, `
		INSERT INTO context (ctx_hash, ctx_type, data_blob, refs_json, created_at, last_access, access_count)
		VALUES (?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(ctx_hash) DO UPDATE SET
			ctx_type=excluded.ctx_type, data_blob=excluded.data_blob,
			refs_json=excluded.refs_json, last_access=excluded.last_access`,
		hash, typ, data, string(refsJSON), now, now)
	if err != nil {
		return fmt.Errorf("put context: %w", err)
	}
	if err := s.evictTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns a snapshot and bumps its access recency, so the LRU clock sees
// the read. ok is false when the hash is absent.
func (s *Store) Get(ctx context.Context, hash string) (Entry, bool, error) {
	var e Entry
	var refsJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT ctx_hash, ctx_type, data_blob, refs_json FROM context WHERE ctx_hash = ?`, hash).
		Scan(&e.Hash, &e.Type, &e.Data, &refsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("get context: %w", err)
	}
	_ = json.Unmarshal([]byte(refsJSON), &e.Refs)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE context SET last_access=?, access_count=access_count+1 WHERE ctx_hash=?`,
		storage.Now(), hash); err != nil {
		return Entry{}, false, fmt.Errorf("touch context: %w", err)
	}
	return e, true, nil
}

// Contains reports whether hash is present without bumping access counters.
func (s *Store) Contains(ctx context.Context, hash string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM context WHERE ctx_hash = ?`, hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("contains context: %w", err)
	}
	return true, nil
}

// evictTx is evict inside Put's transaction (P2-14).
func (s *Store) evictTx(ctx context.Context, tx *sql.Tx) error {
	if s.max <= 0 {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM context`).Scan(&count); err != nil {
		return fmt.Errorf("count context: %w", err)
	}
	if count <= s.max {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ctx_hash FROM context ORDER BY last_access ASC, access_count ASC LIMIT ?`,
		count-s.max)
	if err != nil {
		return fmt.Errorf("select evict: %w", err)
	}
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		hashes = append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(hashes) == 0 {
		return nil
	}
	// Batch the eviction into a single DELETE to minimize round-trips and
	// write amplification on embedded SQLite (SD-card storage).
	args := make([]any, len(hashes))
	ph := strings.TrimSuffix(strings.Repeat("?,", len(hashes)), ",")
	for i, h := range hashes {
		args[i] = h
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM context WHERE ctx_hash IN (`+ph+`)`, args...); err != nil {
		return fmt.Errorf("evict: %w", err)
	}
	return nil
}

// Snapshot is the canonical full-context payload before serialization. Data is
// the type-specific snapshot body (e.g. a serialized file context); the store
// treats it as opaque and keys it by the SHA-256 of the whole snapshot.
type Snapshot struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	Refs []string        `json:"refs,omitempty"`
}

// Pack serializes a snapshot and returns its SHA-256 hash and blob.
func Pack(s Snapshot) (hash string, blob []byte, err error) {
	blob, err = json.Marshal(s)
	if err != nil {
		return "", nil, fmt.Errorf("pack context: %w", err)
	}
	return Hash(blob), blob, nil
}

// Hash returns the SHA-256 hex digest of a blob. Used to verify a fetched
// snapshot matches the hash advertised in the pointer.
func Hash(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}
