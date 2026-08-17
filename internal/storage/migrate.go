package storage

import (
	"database/sql"
	"fmt"
)

// Migrate applies pending schema migrations and advances PRAGMA user_version.
// It is safe to call repeatedly: already-applied versions are skipped.
//
// Migrations are defined in migrations.go. Each migration runs in its own
// transaction and updates user_version on success so a partially failed run
// can be retried without double-applying earlier steps.
func Migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("migrate: read user_version: %w", err)
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

func runMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := m.Apply(tx); err != nil {
		return err
	}

	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.Version)); err != nil {
		return fmt.Errorf("set user_version %d: %w", m.Version, err)
	}

	return tx.Commit()
}
