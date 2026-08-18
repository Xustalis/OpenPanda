package main

// `panda uninstall` removes OpenPanda with a hard safety contract:
//
//  1. Scan first — the full plan (what dies, what stays, where the backup
//     lands) is printed before anything is touched.
//  2. Explicit confirmation — the user must type `confirm`; piped/empty
//     input aborts unless --yes was passed for scripted runs.
//  3. Whitelist only — deletions come exclusively from Scan's explicit
//     inputs (installed binary, PATH registration, config-declared state
//     files) after guardrails flip anything overlapping the home directory
//     or user assets (memory/, projects/, skills/, work dirs) to "keep".
//  4. Backup by default — the data/config items slated for deletion are
//     zipped to the home directory before removal.
//  5. Report — every deleted and kept item is written to a report file the
//     user can keep.

import (
	"bufio"
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
	yes := fs.Bool("yes", false, "skip the interactive confirmation (for scripts)")
	noBackup := fs.Bool("no-backup", false, "delete without writing a backup zip")
	dryRun := fs.Bool("dry-run", false, "print the plan and exit without deleting anything")
	fs.Parse(args)

	loc := i18n.Detect()

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

	home, _ := os.UserHomeDir()
	stamp := time.Now().Format("20060102-150405")
	backupPath := filepath.Join(home, "openpanda-backup-"+stamp+".zip")
	reportPath := filepath.Join(home, "openpanda-uninstall-report-"+stamp+".txt")
	if *noBackup {
		fmt.Println(i18n.T(loc, "uninstall.backup.skip"))
	} else {
		fmt.Println(i18n.Tf(loc, "uninstall.backup.at", "path", backupPath))
	}

	if *dryRun {
		fmt.Println(i18n.T(loc, "uninstall.dryrun"))
		return
	}

	// ---- confirmation ----
	if !*yes {
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
	}

	// ---- execute ----
	backupCount := 0
	if !*noBackup {
		n, err := install.BackupZip(backupPath, targets)
		if err != nil {
			// A failed backup must not stop the uninstall halfway, but it
			// must be loud — the user asked for reversibility.
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "uninstall.backup.fail", "err", err.Error()))
		}
		backupCount = n
	}

	install.StopServices()

	var deleted, kept, failed []string
	for _, t := range targets {
		switch {
		case t.Kind == install.KindPath:
			continue // handled below via the per-OS editors
		case !t.Delete:
			kept = append(kept, describeTarget(t))
		default:
			if err := install.RemoveOne(t.Path); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", t.Path, err))
			} else if t.Exists {
				deleted = append(deleted, describeTarget(t))
			}
		}
	}
	if changed, err := install.RemovePATHPersistence(dir); err == nil {
		for _, c := range changed {
			deleted = append(deleted, c+" (PATH)")
		}
	}

	// ---- report ----
	var b strings.Builder
	fmt.Fprintf(&b, "OpenPanda uninstall report — %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "DELETED (%d):\n", len(deleted))
	for _, d := range deleted {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	fmt.Fprintf(&b, "\nKEPT — user assets (%d):\n", len(kept))
	for _, k := range kept {
		fmt.Fprintf(&b, "  %s\n", k)
	}
	if !*noBackup {
		fmt.Fprintf(&b, "\nBACKUP: %s (%d files)\n", backupPath, backupCount)
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED (%d):\n", len(failed))
		for _, f := range failed {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	_ = os.WriteFile(reportPath, []byte(b.String()), 0o644)

	fmt.Println(i18n.Tf(loc, "uninstall.done", "path", reportPath))
	if len(failed) > 0 {
		os.Exit(1)
	}
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
