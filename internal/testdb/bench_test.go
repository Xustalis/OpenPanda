// SPDX-License-Identifier: AGPL-3.0-or-later

package testdb

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// BenchmarkOpenTestDB tracks the setup cost paid by each core fixture.
func BenchmarkOpenTestDB(b *testing.B) {
	Open(b).Close() // Build the shared template outside the timed loop.
	for b.Loop() {
		db := Open(b)
		db.Close()
	}
}

// BenchmarkFileSetup compares identical file-backed semantics, excluding the
// once-per-process template migration from steady-state fixture cost.
func BenchmarkFileSetup(b *testing.B) {
	Open(b).Close()
	for _, clone := range []bool{false, true} {
		name := "migrate"
		if clone {
			name = "clone"
		}
		b.Run(name, func(b *testing.B) {
			root := b.TempDir()
			i := 0
			for b.Loop() {
				path := filepath.Join(root, strconv.Itoa(i)+".db")
				i++
				var db *sql.DB
				if clone {
					db = OpenFile(b, path)
				} else {
					var err error
					db, err = storage.Open(path)
					if err != nil {
						b.Fatal(err)
					}
					if err := storage.Migrate(db); err != nil {
						db.Close()
						b.Fatal(err)
					}
				}
				db.Close()
			}
		})
	}
}
