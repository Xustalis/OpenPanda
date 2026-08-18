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
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/i18n"
	"github.com/xenith/openpanda/internal/install"
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
// any check fails so scripts can gate on it.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)

	loc := i18n.Detect()
	fmt.Println(i18n.T(loc, "doctor.title"))
	problems := 0
	fail := func(key string, pairs ...string) {
		problems++
		fmt.Println("  ✗ " + i18n.Tf(loc, key, pairs...))
	}
	pass := func(key string, pairs ...string) {
		fmt.Println("  ✓ " + i18n.Tf(loc, key, pairs...))
	}

	exe, _ := os.Executable()
	pass("doctor.exe", "path", exe)

	dir, err := install.Dir()
	if err == nil {
		bin := filepath.Join(dir, install.ExeName())
		if _, err := os.Stat(bin); err == nil {
			if out, err := install.Verify(bin); err == nil {
				pass("doctor.installed", "path", bin, "out", out)
			} else {
				fail("doctor.installed.fail", "path", bin, "err", err.Error())
			}
		} else {
			fail("doctor.notinstalled", "path", bin)
		}
		if lp, err := exec.LookPath(install.ExeName()); err == nil {
			pass("doctor.path.ok", "path", lp)
		} else {
			fail("doctor.path.no")
		}
		if where := install.PathPersistedAt(dir); len(where) > 0 {
			pass("doctor.persist.ok", "where", joinPaths(where))
		} else {
			fail("doctor.persist.no")
		}
	} else {
		fail("doctor.persist.no")
	}

	if cfg, err := config.Load(*configPath); err == nil {
		pass("doctor.config.ok", "path", configFileUsed(*configPath), "name", cfg.Node.Name)
		if st, err := os.Stat(cfg.Storage.DBPath); err == nil {
			pass("doctor.db.ok", "path", cfg.Storage.DBPath, "size", fmt.Sprintf("%d B", st.Size()))
		} else {
			fail("doctor.db.no", "path", cfg.Storage.DBPath)
		}
		if cfg.Model.APIKey != "" {
			pass("doctor.modelkey.ok")
		} else {
			fail("doctor.modelkey.no")
		}
	} else {
		fail("doctor.config.no", "err", err.Error())
	}

	// Adapter runtime: the agent adapters are Python scripts next to the
	// daemon's working directory, driven by python3, and they wrap the
	// agent CLIs (claude / opencode). Each is reported; only "no agent CLI
	// at all" counts as a problem — native-only nodes stay valid.
	if lp, err := exec.LookPath("python3"); err == nil {
		pass("doctor.python3.ok", "path", lp)
	} else {
		fail("doctor.python3.no")
	}
	if dir := findAdaptersDir(); dir != "" {
		pass("doctor.adapters.ok", "path", dir)
	} else {
		fail("doctor.adapters.no")
	}
	agentsFound := 0
	for _, bin := range []string{"claude", "opencode"} {
		if lp, err := exec.LookPath(bin); err == nil {
			agentsFound++
			pass("doctor.agent.ok", "name", bin, "path", lp)
		} else {
			pass("doctor.agent.no", "name", bin)
		}
	}
	if agentsFound == 0 {
		fail("doctor.agent.none")
	}

	if problems == 0 {
		fmt.Println(i18n.T(loc, "doctor.pass"))
		return
	}
	fmt.Println(i18n.Tf(loc, "doctor.fail", "n", fmt.Sprint(problems)))
	os.Exit(1)
}

// findAdaptersDir locates the adapters/ directory — relative to the working
// directory first (the documented run-from-repo-root layout), then next to
// the executable (a relocated install). Empty when not found.
func findAdaptersDir() string {
	candidates := []string{"adapters"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "adapters"))
	}
	for _, dir := range candidates {
		if st, err := os.Stat(filepath.Join(dir, "claude_code.py")); err == nil && !st.IsDir() {
			return dir
		}
	}
	return ""
}

// configFileUsed mirrors config.Load's resolution for display purposes.
func configFileUsed(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	return config.DefaultPath
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
