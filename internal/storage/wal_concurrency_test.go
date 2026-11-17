// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Observe the first version read or transaction begin while retaining the real
// SQLite driver and lock contention. No sleep or probabilistic race is needed.
type walObserverConnector struct {
	dsn      string
	driver   sqlite.Driver
	observed chan struct{}
	once     sync.Once
}

func (c *walObserverConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &walObserverConn{Conn: conn, owner: c}, nil
}

func (c *walObserverConnector) Driver() driver.Driver { return &c.driver }
func (c *walObserverConnector) notify()               { c.once.Do(func() { close(c.observed) }) }

type walObserverConn struct {
	driver.Conn
	owner *walObserverConnector
}

func (c *walObserverConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *walObserverConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.owner.notify()
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *walObserverConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *walObserverConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err == nil && query == "PRAGMA user_version" {
		return &walObserverRows{Rows: rows, owner: c.owner}, nil
	}
	return rows, err
}

type walObserverRows struct {
	driver.Rows
	owner *walObserverConnector
}

func (r *walObserverRows) Close() error {
	err := r.Rows.Close()
	r.owner.notify() // Row.Scan closes only after reading the snapshot's value.
	return err
}

func TestWALInitializationPreservesConcurrentSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	observer := &walObserverConnector{dsn: "file:" + escapeDBPath(path) + "?_pragma=busy_timeout(5000)&_txlock=immediate", observed: make(chan struct{})}
	other := sql.OpenDB(observer)
	t.Cleanup(func() { _ = other.Close() })
	if err := other.Ping(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		forceWALSidecars(other)
	}()
	select {
	case <-observer.observed:
	case <-ctx.Done():
		t.Fatal("WAL initialization never started")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("WAL initialization did not finish")
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("WAL initialization reverted concurrent migration: user_version = %d, want 2", version)
	}
}

func TestCheckpointReportsBusyReader(t *testing.T) {
	for _, mode := range []string{"FULL", "RESTART", "TRUNCATE"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "busy.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TABLE sample (value INTEGER)"); err != nil {
				t.Fatal(err)
			}
			other, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			other.SetMaxOpenConns(1)
			if _, err := other.Exec("PRAGMA busy_timeout = 1"); err != nil {
				t.Fatal(err)
			}
			reader, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := reader.ExecContext(t.Context(), "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer reader.ExecContext(context.Background(), "ROLLBACK")
			var count int
			if err := reader.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sample").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO sample VALUES (1)"); err != nil {
				t.Fatal(err)
			}
			if err := Checkpoint(t.Context(), other, "PASSIVE"); err != nil {
				t.Fatalf("passive checkpoint should tolerate an older reader: %v", err)
			}
			if err := Checkpoint(t.Context(), other, mode); !errors.Is(err, ErrCheckpointBusy) {
				t.Fatalf("checkpoint %s falsely reported completion while a reader held the WAL: %v", mode, err)
			}
			if _, err := reader.ExecContext(t.Context(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if err := Checkpoint(t.Context(), other, mode); err != nil {
				t.Fatalf("checkpoint after reader release: %v", err)
			}
		})
	}
}
