// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
)

// Compare observing a completed exchange field by field with a single
// coherent snapshot. Setup and the immutable schema are outside the loop.
func BenchmarkDirSyncObservation(b *testing.B) {
	db := openTestDB(b)
	ctx := context.Background()
	pub, sig := strings.Repeat("a", 64), strings.Repeat("b", 128)
	raw := `{"id":"ghost","pub_key":"` + pub + `","sig":"` + sig + `"}`
	if _, err := db.Exec(`INSERT INTO employee_cache (id, pub_key, learned_via, dir_sig, dir_row_json)
		VALUES ('ghost', ?, 'relay', ?, ?)`, pub, sig, raw); err != nil {
		b.Fatal(err)
	}
	b.Run("fields", func(b *testing.B) {
		// The old success path reads pub_key in the loop and again at the
		// assertion, then reads provenance and signature separately.
		queries := []string{
			`SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id=?`,
			`SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id=?`,
			`SELECT COALESCE(learned_via,'') FROM employee_cache WHERE id=?`,
			`SELECT COALESCE(dir_sig,'') FROM employee_cache WHERE id=?`,
		}
		for b.Loop() {
			for _, query := range queries {
				var value string
				if err := db.QueryRowContext(ctx, query, "ghost").Scan(&value); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("snapshot", func(b *testing.B) {
		for b.Loop() {
			if _, err := readSignedDirSnapshot(ctx, db, "ghost"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
