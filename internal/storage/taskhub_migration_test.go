// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import "testing"

func TestMigrateTaskhubPreservesLegacyAndDanglingLinks(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Upgrade the actual previous schema, including a dangling compatibility ID.
	for _, m := range migrations {
		if m.Version > 68 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Apply(tx); err != nil {
			tx.Rollback()
			t.Fatal(m.Version, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`PRAGMA user_version=68`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO todos(title,task_id,created_at,updated_at) VALUES('old intention','missing-task',1,1)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var state, task, key string
	if err = db.QueryRow(`SELECT state,task_id,task_key FROM execution_requests WHERE request_id='legacy:todo:1'`).Scan(&state, &task, &key); err != nil {
		t.Fatal(err)
	}
	if state != "unavailable" || task != "missing-task" || key != "local/local/local/1" {
		t.Fatal(state, task, key)
	}
	var n, revision int
	if err = db.QueryRow(`SELECT count(*) FROM execution_links`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = db.QueryRow(`SELECT work_revision FROM todos WHERE id=1`).Scan(&revision); err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	if _, err = db.Exec(`UPDATE todos SET title='changed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT work_revision FROM todos WHERE id=1`).Scan(&revision); err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
}
