package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/updater"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

// runUpdate handles `panda update [check|apply] [--pre] [--force]`
func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	preFlag := fs.Bool("pre", false, "include pre-release versions (alpha, beta, rc, preview)")
	forceFlag := fs.Bool("force", false, "force download/apply even if version is equal or older")
	checkOnlyFlag := fs.Bool("check", false, "check for updates only without applying")
	fs.Parse(reorderFlags(args, nil))

	subArgs := fs.Args()
	action := "check"
	if len(subArgs) > 0 {
		switch subArgs[0] {
		case "check":
			action = "check"
		case "apply":
			action = "apply"
		default:
			action = subArgs[0]
		}
	}
	if *checkOnlyFlag {
		action = "check"
	}

	switch action {
	case "check":
		executeCheck(*preFlag)
	case "apply":
		executeApply(*preFlag, *forceFlag)
	default:
		loc := i18n.Detect()
		p := palFor(os.Stderr)
		fmt.Fprintf(os.Stderr, "panda: %s %s\n", i18n.T(loc, "cli.unknownSub"), p.Command("update "+action))
		fmt.Fprintf(os.Stderr, "  %s %s | %s\n", i18n.T(loc, "cli.help.more"), p.Command("panda update check"), p.Command("panda update apply"))
		os.Exit(2)
	}
}

// runUpgrade handles `panda upgrade [--pre] [--force]`
func runUpgrade(args []string) {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	preFlag := fs.Bool("pre", false, "include pre-release versions (alpha, beta, rc, preview)")
	forceFlag := fs.Bool("force", false, "force download/apply even if version is equal or older")
	fs.Parse(args)

	executeApply(*preFlag, *forceFlag)
}

func executeCheck(includePre bool) {
	loc := i18n.Detect()
	p := palFor(os.Stdout)

	if !jsonOutput {
		fmt.Println(p.Muted(i18n.T(loc, "cli.update.checking")))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := updater.New(updater.Options{
		Current:           versionpkg.Version,
		IncludePrerelease: includePre,
	})

	if err := m.Check(ctx); err != nil {
		if jsonOutput {
			emitJSON(m.Status())
		} else {
			fmt.Fprintln(os.Stderr, p.Danger(i18n.Tf(loc, "cli.update.failed", "err", err.Error())))
		}
		os.Exit(1)
	}

	st := m.Status()
	if jsonOutput {
		emitJSON(st)
		return
	}

	if st.Available {
		fmt.Println(p.Heading(i18n.Tf(loc, "cli.update.available", "latest", "v"+st.Latest, "current", "v"+st.Current)))
		if st.Notes != "" {
			fmt.Println()
			fmt.Println(p.Bold(i18n.T(loc, "cli.update.notes")))
			for _, line := range strings.Split(st.Notes, "\n") {
				fmt.Println("  " + line)
			}
		}
		fmt.Println()
		fmt.Println(p.Muted(i18n.T(loc, "cli.update.applyHint")))
		fmt.Println("  " + p.Command("panda update apply") + "  " + p.Muted("(or: "+p.Command("panda upgrade")+")"))
	} else {
		if st.Notes != "" && strings.HasPrefix(st.Notes, "暂无法检查更新") {
			fmt.Println(p.Warn(st.Notes))
		} else {
			fmt.Println(p.Success(i18n.Tf(loc, "cli.update.uptodate", "current", "v"+st.Current)))
		}
	}
}

func executeApply(includePre, force bool) {
	loc := i18n.Detect()
	p := palFor(os.Stdout)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	opts := updater.Options{
		Current:           versionpkg.Version,
		IncludePrerelease: includePre,
		NoRestart:         true,
		Force:             force,
	}
	if floor, err := currentSchemaFloor(); err == nil {
		opts.SchemaFloor = func(context.Context) (int, error) { return floor, nil }
	}

	m := updater.New(opts)

	if !jsonOutput {
		fmt.Println(p.Muted(i18n.T(loc, "cli.update.checking")))
	}

	if err := m.Check(ctx); err != nil {
		if jsonOutput {
			emitJSON(m.Status())
		} else {
			fmt.Fprintln(os.Stderr, p.Danger(i18n.Tf(loc, "cli.update.failed", "err", err.Error())))
		}
		os.Exit(1)
	}

	st := m.Status()
	if !st.Available && !force {
		if jsonOutput {
			emitJSON(st)
		} else {
			if st.Notes != "" && strings.HasPrefix(st.Notes, "暂无法检查更新") {
				fmt.Println(p.Warn(st.Notes))
			} else {
				fmt.Println(p.Success(i18n.Tf(loc, "cli.update.uptodate", "current", "v"+st.Current)))
			}
		}
		return
	}

	targetVersion := st.Latest
	if targetVersion == "" {
		targetVersion = st.Current
	}

	if !jsonOutput {
		fmt.Println(p.Info(i18n.Tf(loc, "cli.update.downloading", "version", "v"+targetVersion)))
	}

	if err := m.DownloadForce(ctx, force); err != nil {
		if jsonOutput {
			emitJSON(m.Status())
		} else {
			fmt.Fprintln(os.Stderr, p.Danger(i18n.Tf(loc, "cli.update.failed", "err", err.Error())))
		}
		os.Exit(1)
	}

	if !jsonOutput {
		fmt.Println(p.Info(i18n.T(loc, "cli.update.applying")))
	}

	if err := m.Apply(ctx); err != nil {
		if jsonOutput {
			emitJSON(m.Status())
		} else {
			fmt.Fprintln(os.Stderr, p.Danger(i18n.Tf(loc, "cli.update.failed", "err", err.Error())))
		}
		os.Exit(1)
	}

	if jsonOutput {
		emitJSON(m.Status())
	} else {
		fmt.Println(p.Success(i18n.Tf(loc, "cli.update.success", "version", "v"+targetVersion)))
		fmt.Println(p.Muted(i18n.T(loc, "cli.update.restartHint")))
	}
}

// schemaFloorFunc adapts an open store handle to Options.SchemaFloor — the
// updater reads the data directory's schema version through it before
// swapping in a staged release.
func schemaFloorFunc(db *sql.DB) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		var v int
		if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
			return 0, fmt.Errorf("read user_version: %w", err)
		}
		return v, nil
	}
}

// currentSchemaFloor reads the configured database's user_version for the
// CLI apply path, which has no store open otherwise. Best-effort: a
// config/DB read failure returns an error and the caller leaves
// Options.SchemaFloor unset; storage.Open never migrates, so this is a
// read-only probe. A missing DB file is skipped rather than created — the
// probe must not leave an empty database behind on a fresh machine.
func currentSchemaFloor() (int, error) {
	cfg, err := loadConfigQuietly(cliConfigPath)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(cfg.Storage.DBPath); err != nil {
		return 0, err
	}
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
