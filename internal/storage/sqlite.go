// Package storage wraps the local SQLite database (WAL mode).
//
// A pure-Go driver (modernc.org/sqlite) is used so the binary cross-compiles
// to linux/arm64 without a C toolchain, which the design doc requires for
// deploying to Orange Pi / Raspberry Pi nodes.
package storage

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) the SQLite database at path in WAL mode.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // single writer avoids SQLITE_BUSY under WAL
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return db, nil
}

// Now returns Unix seconds; SQLite timestamps are stored as epoch seconds.
func Now() int64 { return time.Now().Unix() }
