// SPDX-License-Identifier: AGPL-3.0-or-later

package testdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestTestDBSchemaMatchesMigrations(t *testing.T) {
	for _, path := range []string{":memory:", filepath.Join(t.TempDir(), "fresh.db")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fresh, err := storage.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if err := storage.Migrate(fresh); err != nil {
				t.Fatal(err)
			}
			var clone *sql.DB
			if path == ":memory:" {
				clone = Open(t)
			} else {
				clone = OpenFile(t, filepath.Join(t.TempDir(), "clone.db"))
			}
			schema := func(db *sql.DB) [][4]string {
				rows, err := db.Query(`SELECT type, name, tbl_name, coalesce(sql, '') FROM sqlite_schema ORDER BY type, name`)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var result [][4]string
				for rows.Next() {
					var row [4]string
					if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
						t.Fatal(err)
					}
					result = append(result, row)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return result
			}
			if !reflect.DeepEqual(schema(fresh), schema(clone)) {
				t.Fatal("snapshot lost migrated tables, indexes or triggers")
			}
			for _, pragma := range []string{"user_version", "foreign_keys", "journal_mode"} {
				var want, got string
				if err := fresh.QueryRow("PRAGMA " + pragma).Scan(&want); err != nil {
					t.Fatal(err)
				}
				if err := clone.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s = %s, want %s", pragma, got, want)
				}
			}
		})
	}
}

func TestTestDBIsolation(t *testing.T) {
	for _, file := range []bool{false, true} {
		for i := 0; i < 4; i++ {
			t.Run("private-copy", func(t *testing.T) {
				t.Parallel()
				open := func() *sql.DB {
					if file {
						return OpenFile(t, filepath.Join(t.TempDir(), "private.db"))
					}
					return Open(t)
				}
				db := open()
				if _, err := db.Exec(`CREATE TABLE fixture_probe(value TEXT); INSERT INTO fixture_probe VALUES ('private')`); err != nil {
					t.Fatal(err)
				}
				// A later clone must see neither this schema mutation nor its data.
				other := open()
				var count int
				if err := other.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='fixture_probe'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("test databases share schema or mutated the template")
				}
				if err := db.QueryRow(`SELECT count(*) FROM fixture_probe WHERE value='private'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("private data changed: count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestFileDBWALAndReopen(t *testing.T) {
	ctx := context.Background()
	dir := "nested %#"
	if runtime.GOOS != "windows" {
		dir += "?"
	}
	path := filepath.Join(t.TempDir(), dir, "fixture.db")
	db := OpenFile(t, path)
	if db.Stats().MaxOpenConnections != 4 {
		t.Fatal("file fixture lost storage connection pool")
	}
	if _, err := db.Exec("CREATE TABLE fixture_probe(value INTEGER); INSERT INTO fixture_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	other, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE fixture_probe SET value=2"); err != nil {
		t.Fatal(err)
	}
	// WAL readers on another handle must proceed while a writer is active.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var value int
	if err := other.QueryRowContext(readCtx, "SELECT value FROM fixture_probe").Scan(&value); err != nil || value != 1 {
		t.Fatalf("read during write: value=%d err=%v", value, err)
	}
	// A second pooled connection must retain connection-local storage pragmas.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)
	conn.Close()
	if err != nil || foreignKeys != 1 {
		t.Fatalf("pooled foreign keys: %d %v", foreignKeys, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Checkpoint(ctx, db, "TRUNCATE"); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []*sql.DB{other, db} {
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.QueryRow("SELECT value FROM fixture_probe").Scan(&value); err != nil || value != 2 {
		t.Fatalf("reopen lost committed data: value=%d err=%v", value, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestWriteImageRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeImage(path, []byte("replace")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}
