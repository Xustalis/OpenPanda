package main

// `panda uninstall` removes OpenPanda with a hard safety contract:
//
//  1. Scan first — the full plan (what dies, what stays, where the backup
//     lands) is printed before anything is touched.
//  2. Explicit confirmation — the user must type `confirm`; piped/empty
//     input aborts unless --yes was passed for scripted runs. `--purge`
//     (also delete user data) demands a second, distinct confirmation and
//     aborts the ENTIRE uninstall — backup included — when refused.
//  3. Whitelist only — deletions come exclusively from Scan's explicit
//     inputs (installed binary, PATH registration, config-declared state
//     files) after guardrails flip anything overlapping the home directory
//     or user assets (memory/, projects/, skills/, work dirs) to "keep".
//  4. Backup by default — the data/config items slated for deletion are
//     zipped to the home directory before removal (on a --purge run the
//     user assets are archived too). --backup-only writes the zip and
//     touches nothing else.
//  5. Report — every deleted and kept item is written to a report file the
//     user can keep.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/install"
)

func runUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	yes := fs.Bool("yes", false, "skip the interactive confirmation(s) (for scripts)")
	noBackup := fs.Bool("no-backup", false, "delete without writing a backup zip")
	dryRun := fs.Bool("dry-run", false, "print the plan and exit without deleting anything")
	purge := fs.Bool("purge", false, "also delete user data (memory/projects/skills/work); requires a second confirmation (or --yes)")
	backupOnly := fs.Bool("backup-only", false, "write the backup zip but delete nothing")
	fs.Parse(args)

	loc := i18n.Detect()

	if *purge && *backupOnly {
		fatal("uninstall", errors.New(i18n.T(loc, "cli.uninstall.purgeConflict")))
	}
	if *backupOnly && *noBackup {
		fatal("uninstall", errors.New(i18n.T(loc, "cli.uninstall.backupConflict")))
	}

	// Best-effort config: an unreadable config must not block removing the
	// binary and PATH entries — the plan simply lists no storage items.
	var storage *install.StoragePaths
	var cfgFile string
	if cfg, err := config.Load(*configPath); err == nil {
		cfgFile = configFileUsed(*configPath)
		storage = &install.StoragePaths{
			DBPath:       cfg.Storage.DBPath,
			ContextPath:  cfg.Storage.ContextPath,
			MemoryPath:   cfg.Storage.MemoryPath,
			ProjectsPath: cfg.Storage.ProjectsPath,
			SkillsPath:   cfg.Storage.SkillsPath,
			WorkPath:     cfg.Storage.WorkPath,
			VAPIDKeyPath: cfg.Push.VAPIDKeyPath,
		}
	} else {
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "uninstall.cfgskipped", "err", err.Error()))
	}

	exe, _ := os.Executable()
	dir, err := install.Dir()
	if err != nil {
		fatal("uninstall", err)
	}
	// Resolve the distribution prefix before anything is deleted: once the
	// binary is gone, EvalSymlinks can no longer find it.
	sweepPrefix, sweepable := install.SweepablePrefix(exe)
	targets := install.Scan(install.PlanInput{
		Storage:        storage,
		ConfigFileUsed: cfgFile,
		ExePath:        exe,
		InstallDir:     dir,
	})

	// ---- print the plan ----
	fmt.Println(i18n.T(loc, "uninstall.title"))
	for _, t := range targets {
		if !t.Exists && t.Kind != install.KindPath {
			continue
		}
		if t.Delete {
			fmt.Println("  " + i18n.Tf(loc, "uninstall.delete", "path", t.Path, "kind", t.Kind, "size", humanBytes(t.Bytes)))
		} else {
			fmt.Println("  " + i18n.Tf(loc, "uninstall.keep", "path", t.Path, "reason", t.Reason))
		}
	}

	// The purge preview: user assets --purge would additionally delete.
	purgeAssets := assetTargets(targets)
	if *purge && len(purgeAssets) > 0 {
		fmt.Println("  " + i18n.T(loc, "uninstall.purge.head"))
		for _, t := range purgeAssets {
			fmt.Println("  " + i18n.Tf(loc, "uninstall.purge.item", "path", t.Path, "kind", t.Kind, "size", humanBytes(t.Bytes)))
		}
	}

	home, _ := os.UserHomeDir()
	stamp := time.Now().Format("20060102-150405")
	backupPath := filepath.Join(home, "openpanda-backup-"+stamp+".zip")
	reportPath := filepath.Join(home, "openpanda-uninstall-report-"+stamp+".txt")
	if *noBackup {
		fmt.Println(i18n.T(loc, "uninstall.backup.skip"))
	} else if *backupOnly {
		fmt.Println(i18n.Tf(loc, "uninstall.backuponly", "path", backupPath))
	} else {
		fmt.Println(i18n.Tf(loc, "uninstall.backup.at", "path", backupPath))
	}

	if *dryRun {
		fmt.Println(i18n.T(loc, "uninstall.dryrun"))
		return
	}

	// ---- confirmation ----
	// Backup-only touches nothing destructive, so it needs no prompt.
	if !*backupOnly && !*yes {
		if !stdinIsTTY() {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.T(loc, "uninstall.noninteractive"))
			os.Exit(1)
		}
		fmt.Print(i18n.T(loc, "uninstall.prompt"))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "confirm" {
			fmt.Println(i18n.T(loc, "uninstall.abort"))
			return
		}
		if *purge {
			// Second, distinct gate: refusing it aborts the whole
			// uninstall — nothing is deleted, nothing is backed up.
			fmt.Print(i18n.T(loc, "uninstall.purge.prompt"))
			line, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != "purge" {
				fmt.Println(i18n.T(loc, "uninstall.purge.abort"))
				return
			}
		}
	}

	// ---- execute ----
	if !*backupOnly {
		install.StopServices()
	}
	opt := install.UninstallOptions{Purge: *purge, BackupOnly: *backupOnly}
	if !*noBackup {
		opt.BackupPath = backupPath
	}
	outcome := install.ExecuteUninstall(targets, opt)
	if outcome.BackupErr != nil {
		// A failed backup must not stop the uninstall halfway, but it
		// must be loud — the user asked for reversibility.
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "uninstall.backup.fail", "err", outcome.BackupErr.Error()))
	}

	if *backupOnly {
		fmt.Println(i18n.Tf(loc, "uninstall.backuponly.done", "path", backupPath, "n", fmt.Sprint(outcome.BackupFiles)))
		return
	}

	var deleted []string
	for _, t := range outcome.Deleted {
		deleted = append(deleted, describeTarget(t))
	}
	if changed, err := install.RemovePATHPersistence(dir); err == nil {
		for _, c := range changed {
			deleted = append(deleted, c+" (PATH)")
		}
	}

	// The distribution prefix itself goes away only when the sweep left it
	// empty — os.Remove refuses non-empty dirs, so Linux XDG storage roots
	// sharing the prefix (data/ memory/ …) are untouched by construction.
	if sweepable {
		if err := os.Remove(sweepPrefix); err == nil {
			deleted = append(deleted, sweepPrefix)
		}
	}

	// ---- report ----
	var b strings.Builder
	fmt.Fprintf(&b, "OpenPanda uninstall report — %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "DELETED (%d):\n", len(deleted))
	for _, d := range deleted {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	if len(outcome.Purged) > 0 {
		fmt.Fprintf(&b, "\nPURGED — user data removed via --purge (%d):\n", len(outcome.Purged))
		for _, t := range outcome.Purged {
			fmt.Fprintf(&b, "  %s\n", describeTarget(t))
		}
	}
	fmt.Fprintf(&b, "\nKEPT — user assets (%d):\n", len(outcome.Kept))
	for _, t := range outcome.Kept {
		fmt.Fprintf(&b, "  %s\n", describeTarget(t))
	}
	if !*noBackup {
		fmt.Fprintf(&b, "\nBACKUP: %s (%d files)\n", backupPath, outcome.BackupFiles)
	}
	if len(outcome.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED (%d):\n", len(outcome.Failed))
		for _, f := range outcome.Failed {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	_ = os.WriteFile(reportPath, []byte(b.String()), 0o644)

	fmt.Println(i18n.Tf(loc, "uninstall.done", "path", reportPath))
	if len(outcome.Failed) > 0 {
		os.Exit(1)
	}
}

// assetTargets returns the user-asset entries of a scan — the trees a
// --purge run would additionally delete (and archive) after its own
// confirmation.
func assetTargets(targets []install.Target) []install.Target {
	var out []install.Target
	for _, t := range targets {
		switch t.Kind {
		case install.KindMemory, install.KindProject, install.KindSkill, install.KindWork:
			if t.Exists {
				out = append(out, t)
			}
		}
	}
	return out
}

func describeTarget(t install.Target) string {
	if t.Reason != "" {
		return fmt.Sprintf("%s [%s] — %s", t.Path, t.Kind, t.Reason)
	}
	return fmt.Sprintf("%s [%s, %s]", t.Path, t.Kind, humanBytes(t.Bytes))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
