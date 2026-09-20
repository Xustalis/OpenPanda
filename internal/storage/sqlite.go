// Package storage wraps the local SQLite database (WAL mode).
//
// A pure-Go driver (modernc.org/sqlite) is used so the binary cross-compiles
// to linux/arm64 without a C toolchain, which the design doc requires for
// deploying to Orange Pi / Raspberry Pi nodes.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) the SQLite database at path in WAL mode.
// The parent directory is created automatically so callers never hit
// SQLITE_CANTOPEN (error 14) because a parent directory was missing — this
// is the most common first-run failure when `panda` is invoked from any
// directory other than the project root.
func Open(path string) (*sql.DB, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db dir %s: %w", dir, err)
			}
		}
	}
	// 优化 WAL 模式配置：
	// - wal_autocheckpoint=1000: 每 1000 页触发 checkpoint，避免 WAL 文件过大
	// - busy_timeout=5000: 写入超时 5 秒，避免瞬时冲突
	// - synchronous=NORMAL: 平衡性能与安全性
	// - foreign_keys=ON: 启用外键约束
	// - cache_size=-64000: 使用 64MB 缓存 (负数表示 KB)
	// - temp_store=MEMORY: 临时表存于内存
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(1000)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=cache_size(-64000)&_pragma=temp_store(MEMORY)", escapeDBPath(path))
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

// Checkpoint executes PRAGMA wal_checkpoint with the given mode ("PASSIVE", "FULL", "RESTART", "TRUNCATE").
// Calling Checkpoint with "TRUNCATE" or "PASSIVE" flushes uncommitted and committed WAL pages into the database
// file, preventing unbounded WAL log file growth and ensuring durability before graceful shutdown.
func Checkpoint(ctx context.Context, db *sql.DB, mode string) error {
	if db == nil {
		return nil
	}
	if mode == "" {
		mode = "PASSIVE"
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA wal_checkpoint(%s)", mode))
	return err
}

// escapeDBPath percent-encodes the characters that would otherwise terminate
// the SQLite URI path (% ? #), so a database path containing them is treated as
// a literal filename rather than as query/fragment delimiters (D21).
func escapeDBPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// Now returns Unix seconds; SQLite timestamps are stored as epoch seconds.
func Now() int64 { return time.Now().Unix() }
