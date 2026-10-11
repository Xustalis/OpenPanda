// SPDX-License-Identifier: AGPL-3.0-or-later

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
	// - _txlock=immediate: 写事务在 BEGIN 即取得写锁。连接池放大后这是必需
	//   的——deferred 事务的"先读后写"升级在多连接下会撞上
	//   SQLITE_BUSY_SNAPSHOT（快照冲突，busy_timeout 不覆盖），而
	//   BEGIN IMMEDIATE 把写者间竞争变成 busy_timeout 可重试的锁等待。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(1000)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=cache_size(-64000)&_pragma=temp_store(MEMORY)&_txlock=immediate", escapeDBPath(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if path == ":memory:" {
		// 纯 ":memory:" 每条连接是独立数据库，连接池会让不同连接看到
		// 不同的库——内存库必须保持单连接（测试路径）。
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		// WAL 的单写多读模型：单连接把守护进程、panel、CLI 的所有读写
		// 串行化（大查询会阻塞写入）。4 条连接让读不被写阻塞，写者间
		// 由 BEGIN IMMEDIATE + busy_timeout 串行——与单连接相同的
		// 单写语义，但消除了读-写互相排队。
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4) // 常驻连接，避免每条连接重复跑 _pragma 初始化
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	// The database holds task payloads, session transcripts and memory — the
	// same secret-adjacent material config.yaml gets hardened to 0600 for.
	// A driver-created file lands at the umask (typically 0644), so tighten
	// it here; the WAL/shm sidecars carry the same rows. Best-effort: a
	// read-only filesystem or a foreign-ownership file must not break Open.
	if path != ":memory:" {
		for _, f := range []string{path, path + "-wal", path + "-shm"} {
			_ = os.Chmod(f, 0o600)
		}
	}
	return db, nil
}

// Checkpoint executes PRAGMA wal_checkpoint with the given mode ("PASSIVE",
// "FULL", "RESTART", "TRUNCATE"). "TRUNCATE" or "PASSIVE" flush committed WAL
// pages into the database file, preventing unbounded WAL growth and ensuring
// durability before graceful shutdown. PRAGMA arguments cannot be bound
// parameters, so the mode is whitelisted rather than quoted — only the four
// names SQLite defines may reach the string.
func Checkpoint(ctx context.Context, db *sql.DB, mode string) error {
	if db == nil {
		return nil
	}
	switch mode {
	case "", "PASSIVE":
		mode = "PASSIVE"
	case "FULL", "RESTART", "TRUNCATE":
	default:
		return fmt.Errorf("storage: unknown wal_checkpoint mode %q", mode)
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
