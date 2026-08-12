package core

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/xenith/panda/internal/storage"
)

// testLogger returns a silent logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// verboseTestLogger returns a logger that writes to the test's log output.
func verboseTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

// openTestDB returns an in-memory SQLite DB with the Phase 0 schema applied.
// The driver is registered via storage.Open.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}
