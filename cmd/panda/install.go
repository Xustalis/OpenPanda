package main

// `panda install` places the running binary on PATH persistently (rc-file
// marker block on unix, HKCU\Environment on Windows) and self-verifies the
// installed copy by executing `panda version` through it. `panda doctor`
// is the standalone self-check: it reports whether the command resolves,
// whether the registration survives a reboot, and whether config/database
// are usable. Together they close the loop the user asked for: install →
// verify → (later) diagnose.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/doctor"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/install"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "install directory (default: ~/.local/bin on unix, %LOCALAPPDATA%\\OpenPanda\\bin on Windows)")
	noPath := fs.Bool("no-path", false, "copy the binary but do not register it on PATH")
	fs.Parse(args)

	loc := i18n.Detect()
	dir := *dirFlag
	if dir == "" {
		var err error
		dir, err = install.Dir()
		if err != nil {
			fatal("install", err)
		}
	}
	bin := filepath.Join(dir, install.ExeName())

	if err := install.CopySelf(bin); err != nil {
		fatal("install", err)
	}
	fmt.Println(i18n.Tf(loc, "install.copied", "path", bin))

	if ad := findAdaptersDir(); ad != "" {
		targetDirs := []string{
			filepath.Join(filepath.Dir(dir), "adapters"),
			filepath.Join(filepath.Dir(dir), "share", "openpanda", "adapters"),
		}
		if ucd, err := os.UserConfigDir(); err == nil && ucd != "" {
			targetDirs = append(targetDirs, filepath.Join(ucd, "openpanda", "adapters"))
		}
		for _, td := range targetDirs {
			if !samePath(ad, td) {
				if err := copyAdaptersDir(ad, td); err != nil {
					fmt.Fprintln(os.Stderr, "install: copy adapters: "+err.Error())
				} else {
					fmt.Println(i18n.Tf(loc, "install.copied", "path", td))
				}
			}
		}
	}

	// Register on PATH unless suppressed. Idempotent on both platforms.
	if !*noPath {
		if install.InPATH(dir) {
			fmt.Println(i18n.Tf(loc, "install.path.existed", "dir", dir))
		} else {
			written, err := install.AddToPATH(dir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "panda: "+err.Error())
			} else {
				fmt.Println(i18n.Tf(loc, "install.path.added", "files", joinPaths(written)))
			}
			// The *current* terminal still runs with the old environment —
			// tell the user instead of letting them wonder why `panda`
			// is not found until they open a new one.
			fmt.Println(i18n.Tf(loc, "install.restart", "path", bin))
		}
	}

	// Self-verify: run the installed copy, not the invoked one, so a
	// broken install (bad copy, quarantined by AV, wrong arch) surfaces now.
	if out, err := install.Verify(bin); err != nil {
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "install.verify.fail", "err", err.Error()))
		os.Exit(1)
	} else {
		fmt.Println(i18n.Tf(loc, "install.verify.ok", "out", out))
	}
}

// runDoctor is the post-install / post-update self-check. It exits 1 when
// any check fails so scripts can gate on it. The checks themselves live in
// doctorReport so `/doctor` inside the REPL can print the same report without
// taking the process down with it.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)

	loc := i18n.Detect()
	if problems := doctorReport(loc, *configPath, os.Stdout); problems > 0 {
		fmt.Println(i18n.Tf(loc, "doctor.fail", "n", fmt.Sprint(problems)))
		os.Exit(1)
	}
	fmt.Println(i18n.T(loc, "doctor.pass"))
}

// doctorReport prints the self-check report and returns the number of failed
// checks. The checks themselves live in internal/doctor, shared with the
// panel's /api/doctor, so the terminal and the console can never drift on
// what is checked. The ✓/✗ marks degrade to +/x on a terminal without the
// glyphs and are tinted on a colour one — a page of checks is read by
// scanning for the failures, so they have to stand out.
func doctorReport(loc i18n.Locale, configPath string, out io.Writer) int {
	if out == nil {
		out = io.Discard
	}
	p := pal()
	_, _ = fmt.Fprintln(out, p.Heading(i18n.T(loc, "doctor.title")))
	checks := doctor.Run(configPath)
	for _, c := range checks {
		if c.OK {
			_, _ = fmt.Fprintln(out, "  "+p.Success(p.MarkOK())+" "+i18n.Tf(loc, c.Key, c.Pairs...))
		} else {
			_, _ = fmt.Fprintln(out, "  "+p.Danger(p.MarkFail())+" "+i18n.Tf(loc, c.Key, c.Pairs...))
		}
	}
	return doctor.Problems(checks)
}

// findAdaptersDir locates the adapters/ directory; the search itself moved to
// internal/doctor so `panda doctor` and /api/doctor probe the same places.
func findAdaptersDir() string {
	return doctor.AdaptersDir()
}

func copyAdaptersDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		perm := os.FileMode(0o755)
		if err == nil {
			perm = info.Mode().Perm()
		}
		if err := os.WriteFile(dstPath, data, perm); err != nil {
			return err
		}
	}
	return nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// configFileUsed mirrors config.Load's resolution for display purposes.
func configFileUsed(explicit string) string {
	return config.ResolvePath(explicit)
}

func joinPaths(ps []string) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
