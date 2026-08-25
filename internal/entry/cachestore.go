package entry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"
)

// DiskCache is a small SQLite-backed cache for entry-model decisions
// (intent classification, supervise verdicts). One row is one decision,
// namespaced by ns and keyed by two SHA-256 hashes — the prompt side and the
// device-snapshot / result side — so a changed input naturally lands on a
// different key and misses. Hits skip the LLM call entirely.
//
// The table is created by storage migration v11 (entry_cache). Failures are
// deliberately best-effort: a cache that cannot be read or written degrades
// to a plain uncached call, never an error for the user.
type DiskCache struct {
	db  *sql.DB
	ttl time.Duration
}

// defaultCacheTTL is how long a cached decision stays valid. Inputs are
// hashed into the key, so staleness comes only from external change (a
// different model, prompt wording updates); a bounded TTL keeps that window
// small without any explicit invalidation machinery.
const defaultCacheTTL = 7 * 24 * time.Hour

// NewDiskCache builds a disk cache over an already-migrated database. A nil
// db yields a disabled cache (all operations are no-ops).
func NewDiskCache(db *sql.DB) *DiskCache {
	if db == nil {
		return nil
	}
	return &DiskCache{db: db, ttl: defaultCacheTTL}
}

// Get loads the cached value for (ns, k1, k2) into dst. It reports false on
// a miss, an expired row, or any storage/decode failure — every failure path
// is a miss, never an error.
func (c *DiskCache) Get(ctx context.Context, ns, k1, k2 string, dst any) bool {
	if c == nil || c.db == nil {
		return false
	}
	var blob string
	err := c.db.QueryRowContext(ctx,
		`SELECT output_json FROM entry_cache WHERE ns=? AND k1=? AND k2=? AND created_at >= ?`,
		ns, k1, k2, time.Now().Add(-c.ttl).Unix(),
	).Scan(&blob)
	if err != nil {
		return false
	}
	return json.Unmarshal([]byte(blob), dst) == nil
}

// Put stores v under (ns, k1, k2) and evicts everything past the TTL. Both
// statements are best-effort; a failed write only costs a future miss.
func (c *DiskCache) Put(ctx context.Context, ns, k1, k2 string, v any) {
	if c == nil || c.db == nil {
		return
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return
	}
	now := time.Now().Unix()
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM entry_cache WHERE created_at < ?`, now-int64(c.ttl/time.Second)); err != nil {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO entry_cache (ns, k1, k2, output_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		ns, k1, k2, string(blob), now)
}

// hashString returns the hex SHA-256 of s — the cache key primitive.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
