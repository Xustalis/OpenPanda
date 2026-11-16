// SPDX-License-Identifier: AGPL-3.0-or-later

package testdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// BenchmarkOpenTestDB tracks the setup cost paid by each core fixture.
func BenchmarkOpenTestDB(b *testing.B) {
	image, err := testDBImage() // Build the shared template outside the timed loop.
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		db, err := openMemory(image)
		if err != nil {
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileSetup compares identical file-backed semantics, excluding the
// once-per-process template migration from steady-state fixture cost.
func BenchmarkFileSetup(b *testing.B) {
	image, err := testDBImage()
	if err != nil {
		b.Fatal(err)
	}
	for _, clone := range []bool{false, true} {
		name := "migrate"
		if clone {
			name = "clone"
		}
		b.Run(name, func(b *testing.B) {
			// One live file and no per-iteration cleanup closures: both disk
			// and retained heap stay O(1) as the benchmark scales its count.
			path := filepath.Join(b.TempDir(), "fixture.db")
			for b.Loop() {
				var db *sql.DB
				var err error
				if clone {
					db, err = openFile(path, image)
				} else {
					db, err = storage.Open(path)
					if err == nil {
						err = storage.Migrate(db)
					}
				}
				if err != nil {
					if db != nil {
						db.Close()
					}
					b.Fatal(err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
