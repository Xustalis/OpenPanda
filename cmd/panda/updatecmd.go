// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
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
//   - OPENPANDA_UPDATE_BASE: alternate asset directory for the download,
//     ending in "/releases/download/v<version>". OPENPANDA_RELEASE_BASE — the
//     name scripts/install.ps1 and install.sh already honour — is accepted as
//     an alias so one variable steers both the installer and the updater.
//     This covers the network that can reach api.github.com and github.com but
//     not release-assets.githubusercontent.com, where `panda upgrade` would
//     otherwise fail during the download with no recourse short of a manual
//     install.
//   - OPENPANDA_UPDATE_PUBKEY: hex or base64 Ed25519 public key. When set,
//     a release must carry checksums.txt.sig (a detached signature over
//     checksums.txt) or the update is refused, so a compromised release
//     channel cannot ship a binary this node would install.
//   - OPENPANDA_UPDATE_UNSIGNED: any non-empty value opts into checksum-only
//     updates and clears the configured/baked key for this run — the escape
//     hatch for a self-hosted channel that ships no checksums.txt.sig.
//     Without it (or a key) a download refuses: checksums and the archive
//     cross the same channel, so an unsigned build has no anchor a channel
//     attacker could not forge.
//
// Precedence is explicit Options > environment > the key baked into the
// binary (version.ReleasePubKey, which carries the official channel's key by
// default — release packaging and `make build` alike). The env var still
// lets an operator point a self-hosted channel at their own key.
func updateEnvOptions(opts updater.Options) updater.Options {
	if opts.Repo == "" {
		opts.Repo = os.Getenv("OPENPANDA_UPDATE_REPO")
	}
	if opts.DownloadBase == "" {
		opts.DownloadBase = strings.TrimSpace(os.Getenv("OPENPANDA_UPDATE_BASE"))
		if opts.DownloadBase == "" {
			// The installers' spelling, kept as an alias: a host that already
			// installs from a mirror should not need a second variable to
			// update from it.
			opts.DownloadBase = strings.TrimSpace(os.Getenv("OPENPANDA_RELEASE_BASE"))
		}
	}
	if opts.ReleaseKey == "" {
		opts.ReleaseKey = os.Getenv("OPENPANDA_UPDATE_PUBKEY")
	}
	if opts.ReleaseKey == "" {
		opts.ReleaseKey = versionpkg.ReleasePubKey
	}
	// Parse, don't test presence: OPENPANDA_UPDATE_UNSIGNED=0 used to be a
	// non-empty string and therefore ENABLED checksum-only updates — the
	// opposite of what an operator writing 0 to enforce signatures asked for.
	if v := strings.TrimSpace(os.Getenv("OPENPANDA_UPDATE_UNSIGNED")); v != "" {
		if on, err := strconv.ParseBool(v); err == nil && on {
			opts.AllowUnsigned = true
		}
	}
	return opts
}

// runUpdate handles `panda update [check|apply] [--pre] [--force]`
func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	preFlag := fs.Bool("pre", false, "include pre-release versions (alpha, beta, rc, preview)")
	forceFlag := fs.Bool("force", false, "force download/apply even if version is equal or older")
	checkOnlyFlag := fs.Bool("check", false, "check for updates only without applying")
	fs.Parse(reorderFlags(fs, args))

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
		failf(2, "  %s %s | %s\n", i18n.T(loc, "cli.help.more"), p.Command("panda update check"), p.Command("panda update apply"))
	}
}

// runUpgrade handles `panda upgrade [--pre] [--force]`
func runUpgrade(args []string) {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	preFlag := fs.Bool("pre", false, "include pre-release versions (alpha, beta, rc, preview)")
	forceFlag := fs.Bool("force", false, "force download/apply even if version is equal or older")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda upgrade [--pre] [--force]")

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
		applyCmd, upgradeCmd := updateApplyCommands(includePre)
		fmt.Println("  " + p.Command(applyCmd) + "  " + p.Muted("(or: "+p.Command(upgradeCmd)+")"))
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
				if pre, ok := newerPrerelease(ctx, versionpkg.Version); ok {
					fmt.Println(p.Warn(i18n.Tf(loc, "cli.update.prereleaseHint", "version", verDisplay(pre.Version, pre.Codename))))
					applyCmd, upgradeCmd := updateApplyCommands(true)
					fmt.Println("  " + p.Command(applyCmd) + "  " + p.Muted("(or: "+p.Command(upgradeCmd)+")"))
				} else {
					fmt.Println(p.Success(i18n.Tf(loc, "cli.update.uptodate", "current", versionpkg.Display())))
				}
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

// updateApplyCommands returns the exact commands that re-discover the release
// a check just found. `apply` re-runs the check from scratch, so a --pre check
// must carry the flag into apply; otherwise a stable build only sees the
// stable channel and reports itself up to date.
func updateApplyCommands(includePre bool) (apply, upgrade string) {
	if includePre {
		return "panda update apply --pre", "panda upgrade --pre"
	}
	return "panda update apply", "panda upgrade"
}

// newerPrerelease reports a pre-release newer than current when the stable
// channel has nothing. A bare `apply` is a fresh process with no memory of the
// `check --pre` that found the preview, so without this it would answer "up to
// date" and leave the user with no hint that the preview exists.
func newerPrerelease(ctx context.Context, current string) (updater.Release, bool) {
	if strings.Contains(current, "-") {
		return updater.Release{}, false
	}
	repo := updateEnvOptions(updater.Options{}).Repo
	rel, err := updater.FindLatest(ctx, repo, true, current)
	if err != nil || !strings.Contains(rel.Version, "-") || updater.CompareVersion(rel.Version, current) <= 0 {
		return updater.Release{}, false
	}
	return rel, true
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
