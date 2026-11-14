// SPDX-License-Identifier: AGPL-3.0-or-later

package testdb

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestTestDBSchemaMatchesMigrations(t *testing.T) {
	fresh, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := storage.Migrate(fresh); err != nil {
		t.Fatal(err)
	}
	clone := Open(t)
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
}

func TestTestDBIsolation(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run("private-copy", func(t *testing.T) {
			t.Parallel()
			db := Open(t)
			if _, err := db.Exec(`CREATE TABLE fixture_probe(value TEXT); INSERT INTO fixture_probe VALUES ('private')`); err != nil {
				t.Fatal(err)
			}
			// A later clone must see neither this schema mutation nor its data.
			other := Open(t)
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
