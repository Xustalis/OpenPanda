package storage

import (
	"database/sql"
	"fmt"
)

// MigrationExec is the statement surface a migration body needs: Exec, Query,
// QueryRow — nothing else is used by any of the migrateVN functions. It
// exists so a migration can run inside an explicit BEGIN IMMEDIATE
// transaction driven on the raw *sql.DB (see runMigration), which db.Begin()
// cannot produce (it only issues a plain BEGIN).
type MigrationExec interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Migrate applies pending schema migrations and advances PRAGMA user_version.
// It is safe to call repeatedly: already-applied versions are skipped.
//
// Migrations are defined in migrations.go. Each migration runs in its own
// BEGIN IMMEDIATE transaction and updates user_version on success, so a
// partially failed run can be retried without double-applying earlier steps —
// and two handles migrating the same file serialize on the reserved lock
// instead of colliding at commit.
//
// A database whose user_version is ahead of this binary's migration list is
// an error, not a no-op: running an old binary against a newer schema would
// silently corrupt assumptions the newer schema already made.
func Migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("migrate: read user_version: %w", err)
	}
	latest := migrations[len(migrations)-1].Version
	if version > latest {
		return fmt.Errorf("migrate: database schema version %d is newer than this binary's highest migration (%d) — upgrade the panda binary before starting it on this data directory", version, latest)
	}

	for _, m := range migrations {
		if m.Version <= version {
			continue
		}
		if err := runMigration(db, m); err != nil {
			return fmt.Errorf("migrate v%d %s: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// migrationTx adapts a raw *sql.DB to the MigrationExec surface. Safe only
// because Open pins the pool to one connection, so every statement the Apply
// body runs lands on the same connection the BEGIN ran on.
type migrationTx struct{ db *sql.DB }

func (t migrationTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.db.Exec(query, args...)
}

func (t migrationTx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.db.Query(query, args...)
}

func (t migrationTx) QueryRow(query string, args ...any) *sql.Row {
	return t.db.QueryRow(query, args...)
}

func runMigration(db *sql.DB, m Migration) error {
	// BEGIN IMMEDIATE acquires the reserved write lock before the first
	// statement runs, so concurrent handles migrating the same file queue up
	// here (bounded by the busy_timeout pragma) instead of discovering each
	// other at commit — where a deferred transaction's lock upgrade fails
	// instantly under WAL and cannot be retried.
	if _, err := db.Exec(`BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = db.Exec(`ROLLBACK`)
		}
	}()

	// Re-check user_version inside the lock: between Migrate's outer read and
	// this point, another handle may have applied this migration (or a later
	// one). Running it again would fail loudly at best, corrupt at worst.
	var current int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current >= m.Version {
		if _, err := db.Exec(`COMMIT`); err != nil {
			return fmt.Errorf("commit empty tx: %w", err)
		}
		committed = true
		return nil
	}

	if err := m.Apply(migrationTx{db}); err != nil {
		return err
	}

	// user_version is an int from the compiled-in migration table — no
	// placeholder syntax exists for PRAGMA assignments, and there is no
	// request-controlled input to inject here.
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.Version)); err != nil {
		return fmt.Errorf("set user_version %d: %w", m.Version, err)
	}

	if _, err := db.Exec(`COMMIT`); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}
