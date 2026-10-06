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

// updateEnvOptions layers the release-channel overrides onto opts — the
// knobs an operator uses to harden or self-host the update path:
//
//   - OPENPANDA_UPDATE_REPO: "owner/repo" of the GitHub release channel.
//   - OPENPANDA_UPDATE_PUBKEY: hex or base64 Ed25519 public key. When set,
//     a release must carry checksums.txt.sig (a detached signature over
//     checksums.txt) or the update is refused, so a compromised release
//     channel cannot ship a binary this node would install.
//
// Precedence is explicit Options > environment > the key release packaging
// baked into the binary (version.ReleasePubKey). A build shipped through
// scripts/package.sh with OPENPANDA_RELEASE_KEY set therefore verifies
// signatures out of the box, while the env var still lets an operator point
// a self-hosted channel at their own key.
func updateEnvOptions(opts updater.Options) updater.Options {
	if opts.Repo == "" {
		opts.Repo = os.Getenv("OPENPANDA_UPDATE_REPO")
	}
	if opts.ReleaseKey == "" {
		opts.ReleaseKey = os.Getenv("OPENPANDA_UPDATE_PUBKEY")
	}
	if opts.ReleaseKey == "" {
		opts.ReleaseKey = versionpkg.ReleasePubKey
	}
	return opts
}

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

	m := updater.New(updateEnvOptions(updater.Options{
		Current:           versionpkg.Version,
		CurrentCodename:   versionpkg.Codename,
		IncludePrerelease: includePre,
	}))

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
		fmt.Println(p.Heading(i18n.Tf(loc, "cli.update.available", "latest", verDisplay(st.Latest, st.LatestCodename), "current", versionpkg.Display())))
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
			fmt.Println(p.Success(i18n.Tf(loc, "cli.update.uptodate", "current", versionpkg.Display())))
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
		CurrentCodename:   versionpkg.Codename,
		IncludePrerelease: includePre,
		NoRestart:         true,
		Force:             force,
	}
	if floor, err := currentSchemaFloor(); err == nil {
		opts.SchemaFloor = func(context.Context) (int, error) { return floor, nil }
	}

	m := updater.New(updateEnvOptions(opts))

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
	if !st.Available && (!force || st.Latest == "") {
		if jsonOutput {
			emitJSON(st)
		} else {
			switch {
			case force && st.Latest == "":
				// The check learned no release at all (it degraded on a
				// rate limit or an access denial, leaving Latest empty).
				// --force waives the "is it newer" test, not the need for
				// a known version — there is nothing to download, so
				// surface the reason Check recorded instead of pretending
				// to fetch the running release.
				reason := strings.TrimPrefix(st.Notes, "暂无法检查更新：")
				if reason == "" {
					reason = i18n.T(loc, "cli.update.noRelease")
				}
				fmt.Fprintln(os.Stderr, p.Danger(i18n.Tf(loc, "cli.update.failed", "err", reason)))
			case st.Notes != "" && strings.HasPrefix(st.Notes, "暂无法检查更新"):
				fmt.Println(p.Warn(st.Notes))
			case st.Latest == "":
				fmt.Println(p.Warn(i18n.T(loc, "cli.update.noRelease")))
			default:
				fmt.Println(p.Success(i18n.Tf(loc, "cli.update.uptodate", "current", versionpkg.Display())))
			}
		}
		if st.Latest == "" && force {
			os.Exit(1)
		}
		return
	}

	targetVersion := st.Latest
	targetCodename := st.LatestCodename
	if targetVersion == "" {
		targetVersion = st.Current
		targetCodename = versionpkg.Codename
	}

	if !jsonOutput {
		fmt.Println(p.Info(i18n.Tf(loc, "cli.update.downloading", "version", verDisplay(targetVersion, targetCodename))))
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
		fmt.Println(p.Success(i18n.Tf(loc, "cli.update.success", "version", verDisplay(targetVersion, targetCodename))))
		// Print the release notes of the version just installed — an update
		// that reports nothing new feels like nothing happened, and the notes
		// were already fetched by Check.
		if st.Notes != "" {
			fmt.Println()
			fmt.Println(p.Bold(i18n.T(loc, "cli.update.notes")))
			for _, line := range strings.Split(st.Notes, "\n") {
				fmt.Println("  " + line)
			}
		}
		fmt.Println(p.Muted(i18n.T(loc, "cli.update.restartHint")))
	}
}

// verDisplay renders a version for a human reader: "v" + semver, with the
// release codename appended when the side that owns the release supplied one
// (the GitHub release name carries it; the running binary's is local).
func verDisplay(ver, codename string) string {
	if codename == "" {
		return "v" + ver
	}
	return "v" + ver + " " + codename
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
