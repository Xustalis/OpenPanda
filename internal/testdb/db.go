// SPDX-License-Identifier: AGPL-3.0-or-later

// Package testdb supplies isolated SQLite fixtures for application tests.
// It is imported only by test files; storage migration tests use storage directly.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

// OpenFile creates a fully migrated database at a new path. Unlike Open, it
// retains storage.Open's file-backed WAL and connection pool behavior. Other
// handles and subprocesses can reopen it with storage.Open without migrating.
// The caller owns the directory (normally t.TempDir); an existing file is never
// replaced, so accidentally reinitializing a shared fixture fails immediately.
func OpenFile(t testing.TB, path string) *sql.DB {
	t.Helper()
	image, err := testDBImage()
	if err != nil {
		t.Fatalf("prepare test schema: %v", err)
	}
	if err := writeImage(path, image); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeImage(path string, image []byte) error {
	if path == "" || path == ":memory:" {
		return fmt.Errorf("expected a new file path, got %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(image)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	return closeErr
}
