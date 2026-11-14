// SPDX-License-Identifier: AGPL-3.0-or-later

// Package testdb supplies isolated SQLite fixtures for application tests.
// It is imported only by test files; storage migration tests use storage directly.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Build the empty schema once per test process. Replaying every historical
// migration for every task fixture dominates the race suite. Serialize only
// after the real migration path succeeds; no checked-in schema can drift.
var testDBImage = sync.OnceValues(func() ([]byte, error) {
	db, err := storage.Open(":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		return nil, err
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var image []byte
	err = conn.Raw(func(raw any) error {
		serializer, ok := raw.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return fmt.Errorf("SQLite driver %T does not support Serialize", raw)
		}
		var err error
		image, err = serializer.Serialize()
		return err
	})
	return image, err
})

// Open returns a private, fully migrated in-memory database. The driver
// copies the immutable image into each connection's own SQLite allocation, so
// tests share neither data nor schema mutations. storage tests still exercise
// migrations from scratch and against historical schemas.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	image, err := testDBImage()
	if err != nil {
		t.Fatalf("prepare test schema: %v", err)
	}
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	err = conn.Raw(func(raw any) error {
		restorer, ok := raw.(interface{ Deserialize([]byte) error })
		if !ok {
			return fmt.Errorf("SQLite driver %T does not support Deserialize", raw)
		}
		return restorer.Deserialize(image)
	})
	conn.Close()
	if err != nil {
		t.Fatalf("restore test schema: %v", err)
	}
	return db
}
