package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// openStore prepares the storage roots and returns an opened, migrated
// database handle. It is the single definition of storage startup — the daemon,
// the panel views, the REPL and `panda web` all call it, so the directory
// list and the open/migrate sequence can never drift between them (the
// pre-existing drift this removes: the panel's copy omitted ArtifactPath, so
// a first launch from the panel never created the artifact root).
//
// Storage roots the runtime writes into must exist before any task executes:
// the sandbox sets a child's cwd to work_path, and a missing dir surfaces as
// a misleading fork/exec ENOENT blaming the command binary instead of the
// absent working directory.
func openStore(cfg *config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	for _, dir := range []string{
		cfg.Storage.ContextPath,
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		cfg.Storage.WorkPath,
		cfg.Storage.ArtifactPath,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create storage dir %s: %w", dir, err)
		}
	}
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}
