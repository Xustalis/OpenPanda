package install

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setUserHome pins the user home directory for a test: HOME on unix,
// USERPROFILE on Windows. os.UserHomeDir reads exactly these per-OS, so a
// fake home that only lands in HOME is invisible on Windows and every
// home-derived behavior (owned-config roots, purge guardrails) silently
// tests the real runner home instead.
func setUserHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}

func TestInPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+dir)
	if !InPATH(dir) {
		t.Fatalf("InPATH(%s) = false with PATH containing it", dir)
	}
	if InPATH(t.TempDir()) {
		t.Fatal("InPATH true for a dir not on PATH")
	}
}

// TestAddRemovePATHIdempotent lives in path_unix_test.go: it pins the rc-file
// marked-block behavior that only the unix PATH persistence (path_unix.go)
// has; Windows uses the registry instead.

func TestScanGuardrails(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	setUserHome(t, home)

	db := filepath.Join(root, "data", "openpanda.db")
	os.MkdirAll(filepath.Dir(db), 0o755)
	os.WriteFile(db, []byte("x"), 0o644)

	projects := filepath.Join(root, "projects")
	os.MkdirAll(projects, 0o755)
	os.WriteFile(filepath.Join(projects, "MEMORY.md"), []byte("user asset"), 0o644)

	targets := Scan(PlanInput{
		Storage: &StoragePaths{
			DBPath:       db,
			ContextPath:  filepath.Join(root, "data", "context"),
			MemoryPath:   filepath.Join(root, "memory"),
			ProjectsPath: projects,
			SkillsPath:   filepath.Join(root, "skills"),
			WorkPath:     ".",
		},
		ConfigFileUsed: filepath.Join(root, "config.yaml"), // custom location → keep
		ExePath:        filepath.Join(root, "bin", "panda"),
		InstallDir:     filepath.Join(home, ".local", "bin"),
	})

	find := func(kind, path string) *Target {
		for i, tg := range targets {
			if tg.Kind == kind && tg.Path == filepath.Clean(path) {
				return &targets[i]
			}
		}
		return nil
	}

	if tg := find(KindConfig, filepath.Join(root, "config.yaml")); tg == nil || tg.Delete {
		t.Fatal("custom-located config must be kept")
	}
	if tg := find(KindBinary, filepath.Join(root, "bin", "panda")); tg == nil || !tg.Delete {
		t.Fatal("invoked binary must be a delete target")
	}
	if tg := find(KindProject, projects); tg == nil || tg.Delete {
		t.Fatal("projects dir must be listed as kept asset")
	}
	if tg := find(KindData, db); tg == nil || !tg.Delete {
		t.Fatal("db must be a delete target")
	}
	// Guardrail flip: point context_path at the projects dir — the plan must
	// refuse to delete the asset even though config named it as state.
	targets = Scan(PlanInput{
		Storage:        &StoragePaths{DBPath: db, ContextPath: projects, ProjectsPath: projects},
		ConfigFileUsed: "",
		ExePath:        "",
		InstallDir:     "",
	})
	for _, tg := range targets {
		if tg.Path == filepath.Clean(projects) && tg.Delete {
			t.Fatal("guardrail failed to protect asset dir named as state")
		}
	}
}

// TestScanSweepsDistributionPrefix covers the install.sh / self-update
// layout: the invoked PATH link resolves to <prefix>/bin/panda with
// adapters/ beside it. The plan must sweep bin/ and adapters/ (plus the
// example configs) while leaving everything else in the prefix alone —
// the Linux XDG default puts storage roots in there too.
func TestScanSweepsDistributionPrefix(t *testing.T) {
	home := t.TempDir()
	setUserHome(t, home)

	prefix := t.TempDir() // stands in for ~/.local/share/openpanda
	// Scan resolves through symlinks; macOS TempDir is /var behind /private/var.
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	binDir := filepath.Join(prefix, "bin")
	os.MkdirAll(binDir, 0o755)
	os.WriteFile(filepath.Join(binDir, "panda"), []byte("binary"), 0o755)
	os.MkdirAll(filepath.Join(prefix, "adapters"), 0o755)
	os.WriteFile(filepath.Join(prefix, "adapters", "claude_code.py"), []byte("# adapter"), 0o644)
	os.WriteFile(filepath.Join(prefix, "config.example.yaml"), []byte("# example"), 0o644)
	// Storage the Linux layout would keep inside the same prefix.
	storage := filepath.Join(prefix, "data")
	os.MkdirAll(storage, 0o755)
	os.WriteFile(filepath.Join(storage, "openpanda.db"), []byte("db"), 0o644)

	// The PATH link the user actually invoked.
	linkDir := filepath.Join(home, ".local", "bin")
	os.MkdirAll(linkDir, 0o755)
	link := filepath.Join(linkDir, "panda")
	if err := os.Symlink(filepath.Join(binDir, "panda"), link); err != nil {
		t.Skip("symlinks unavailable")
	}

	targets := Scan(PlanInput{
		ExePath:    link,
		InstallDir: linkDir,
		Storage: &StoragePaths{
			DBPath: filepath.Join(storage, "openpanda.db"),
		},
	})

	must := func(path string, del bool) {
		t.Helper()
		for _, tg := range targets {
			if tg.Path == filepath.Clean(path) {
				if tg.Delete != del {
					t.Fatalf("%s: Delete=%v, want %v", path, tg.Delete, del)
				}
				return
			}
		}
		t.Fatalf("missing target %s", path)
	}
	must(binDir, true)                            // whole bin dir swept
	must(filepath.Join(prefix, "adapters"), true) // adapters swept
	must(filepath.Join(prefix, "config.example.yaml"), true)
	must(filepath.Join(storage, "openpanda.db"), true) // db still targeted as data
	must(link, true)                                   // PATH link removed
	// Nothing may target the prefix itself: the caller only rmdir's it when
	// the sweep left it empty, and the data/ dir keeps it alive here.
	for _, tg := range targets {
		if tg.Path == filepath.Clean(prefix) && tg.Delete {
			t.Fatal("plan must not delete the prefix itself")
		}
	}
}

// TestSweepablePrefixRefusals checks the two layouts that must NOT be
// swept: a Homebrew Cellar (brew owns the files) and a source checkout
// (go.mod / .git beside a locally built bin/panda).
func TestSweepablePrefixRefusals(t *testing.T) {
	setUserHome(t, t.TempDir())

	mkLayout := func(root string) string {
		binDir := filepath.Join(root, "bin")
		os.MkdirAll(binDir, 0o755)
		os.WriteFile(filepath.Join(binDir, "panda"), []byte("x"), 0o755)
		os.MkdirAll(filepath.Join(root, "adapters"), 0o755)
		return filepath.Join(binDir, "panda")
	}

	cellar := mkLayout(filepath.Join(t.TempDir(), "Cellar", "openpanda", "0.0.4"))
	if _, ok := SweepablePrefix(cellar); ok {
		t.Fatal("Homebrew Cellar must not be sweepable")
	}
	if _, ok := distributionPrefix(cellar); !ok {
		t.Fatal("Cellar layout is still a distribution prefix (for the hint)")
	}

	checkout := t.TempDir()
	exe := mkLayout(checkout)
	os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module test"), 0o644)
	if _, ok := SweepablePrefix(exe); ok {
		t.Fatal("source checkout (go.mod) must not be sweepable")
	}
	_ = os.Remove(filepath.Join(checkout, "go.mod"))
	os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir"), 0o644)
	if _, ok := SweepablePrefix(exe); ok {
		t.Fatal("source checkout (.git) must not be sweepable")
	}

	plain := mkLayout(t.TempDir())
	want := filepath.Dir(filepath.Dir(plain))
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved // macOS TempDir sits behind a /var → /private/var link
	}
	if p, ok := SweepablePrefix(plain); !ok || p != want {
		t.Fatalf("plain install prefix not detected: %q (want %q) %v", p, want, ok)
	}
}

// TestScanKeepsHomebrewCellar pins the full-plan behavior for a Homebrew
// install: brew owns the whole keg, so neither the PATH symlink brew link
// created nor anything inside the Cellar may be a delete target — deleting
// them would leave a broken keg brew still tracks. The plan's job is the
// `brew uninstall openpanda` hint.
func TestScanKeepsHomebrewCellar(t *testing.T) {
	home := t.TempDir()
	setUserHome(t, home)

	tmp := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved // macOS TempDir sits behind a /var → /private/var link
	}
	keg := filepath.Join(tmp, "Cellar", "openpanda", "0.0.4")
	binDir := filepath.Join(keg, "bin")
	os.MkdirAll(binDir, 0o755)
	os.WriteFile(filepath.Join(binDir, "panda"), []byte("binary"), 0o755)
	os.MkdirAll(filepath.Join(keg, "adapters"), 0o755)
	os.WriteFile(filepath.Join(keg, "adapters", "codex.py"), []byte("# adapter"), 0o644)

	// The PATH symlink the user actually invoked: /opt/homebrew/bin/panda → keg.
	linkDir := filepath.Join(home, ".local", "bin")
	os.MkdirAll(linkDir, 0o755)
	link := filepath.Join(linkDir, "panda")
	if err := os.Symlink(filepath.Join(binDir, "panda"), link); err != nil {
		t.Skip("symlinks unavailable")
	}

	targets := Scan(PlanInput{
		ExePath:    link,
		InstallDir: linkDir,
	})

	hint := "managed by Homebrew"
	mustKeep := func(path string) {
		t.Helper()
		for _, tg := range targets {
			if tg.Path == filepath.Clean(path) {
				if tg.Delete {
					t.Fatalf("%s: must be kept (brew owns it), got Delete", path)
				}
				if !strings.Contains(tg.Reason, hint) {
					t.Fatalf("%s: reason %q lacks the brew uninstall hint", path, tg.Reason)
				}
				return
			}
		}
		t.Fatalf("missing target %s", path)
	}
	// Neither the PATH link nor the keg prefix is deletable (the prefix entry
	// covers the binary and adapters inside it).
	mustKeep(link)
	mustKeep(keg)
	// And nothing inside the Cellar is swept.
	for _, tg := range targets {
		if tg.Delete && (within(tg.Path, keg) || resolvesInto(tg.Path, keg)) {
			t.Fatalf("plan deletes into the Cellar: %s", tg.Path)
		}
	}
}

func TestBackupZipAndRemoveOne(t *testing.T) {
	root := t.TempDir()
	ctxDir := filepath.Join(root, "context")
	os.MkdirAll(ctxDir, 0o755)
	os.WriteFile(filepath.Join(ctxDir, "a.txt"), []byte("hello"), 0o644)
	db := filepath.Join(root, "openpanda.db")
	os.WriteFile(db, []byte("database"), 0o644)

	// A symlinked "directory" must lose only the link, never the target.
	target := filepath.Join(root, "realdir")
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "precious.txt"), []byte("keep me"), 0o644)
	link := filepath.Join(root, "linkdir")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}

	zipPath := filepath.Join(root, "backup.zip")
	n, err := BackupZip(zipPath, []Target{
		{Path: ctxDir, Kind: KindData, Delete: true, Exists: true},
		{Path: db, Kind: KindData, Delete: true, Exists: true},
		{Path: filepath.Join(root, "bin", "panda"), Kind: KindBinary, Delete: true, Exists: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("backed up %d files, want 2", n)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["context/a.txt"] || !names["openpanda.db"] {
		t.Fatalf("zip contents wrong: %v", names)
	}

	if err := RemoveOne(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("symlink removal destroyed the target dir")
	}
	if data, err := os.ReadFile(filepath.Join(target, "precious.txt")); err != nil || string(data) != "keep me" {
		t.Fatal("symlink removal destroyed target content")
	}
	if err := RemoveOne(ctxDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ctxDir); !os.IsNotExist(err) {
		t.Fatal("real dir not removed")
	}
}

// newUninstallFixture builds the standard uninstall scenario: OpenPanda-owned
// state (db, context dir, config inside the owned ~/.openpanda root) plus
// user-asset dirs (memory/projects/skills/work) holding real files, and
// returns the scanned plan for it.
func newUninstallFixture(t *testing.T) (home, root string, targets []Target) {
	t.Helper()
	home = t.TempDir()
	setUserHome(t, home)
	root = t.TempDir()

	db := filepath.Join(root, "data", "openpanda.db")
	os.MkdirAll(filepath.Dir(db), 0o755)
	os.WriteFile(db, []byte("db"), 0o644)
	ctxDir := filepath.Join(root, "data", "context")
	os.MkdirAll(ctxDir, 0o755)
	os.WriteFile(filepath.Join(ctxDir, "a.txt"), []byte("ctx"), 0o644)
	cfg := filepath.Join(home, ".openpanda", "config.yaml")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("cfg"), 0o644)

	for _, d := range assetDirs(root) {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "KEEP.md"), []byte("user asset"), 0o644)
	}

	targets = Scan(PlanInput{
		Storage: &StoragePaths{
			DBPath:       db,
			ContextPath:  ctxDir,
			MemoryPath:   filepath.Join(root, "memory"),
			ProjectsPath: filepath.Join(root, "projects"),
			SkillsPath:   filepath.Join(root, "skills"),
			WorkPath:     filepath.Join(root, "work"),
		},
		ConfigFileUsed: cfg, // inside ~/.openpanda → owned → delete
		ExePath:        filepath.Join(root, "bin", "panda"),
		InstallDir:     filepath.Join(home, ".local", "bin"),
	})
	return home, root, targets
}

func assetDirs(root string) []string {
	return []string{
		filepath.Join(root, "memory"),
		filepath.Join(root, "projects"),
		filepath.Join(root, "skills"),
		filepath.Join(root, "work"),
	}
}

func zipNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	return names
}

// TestExecuteUninstallNormal is the regression test for a plain uninstall:
// OpenPanda-owned state dies (and lands in the backup), user assets survive.
func TestExecuteUninstallNormal(t *testing.T) {
	home, root, targets := newUninstallFixture(t)
	db := filepath.Join(root, "data", "openpanda.db")
	ctxDir := filepath.Join(root, "data", "context")
	cfg := filepath.Join(home, ".openpanda", "config.yaml")
	backup := filepath.Join(root, "backup.zip")

	out := ExecuteUninstall(targets, UninstallOptions{BackupPath: backup})

	if out.BackupErr != nil || out.BackupFiles != 3 {
		t.Fatalf("backup = %d files, err %v (want 3 files: db, context/a.txt, config.yaml)", out.BackupFiles, out.BackupErr)
	}
	for _, p := range []string{db, ctxDir, cfg} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("owned item %s survived a normal uninstall", p)
		}
	}
	for _, d := range assetDirs(root) {
		if _, err := os.Stat(filepath.Join(d, "KEEP.md")); err != nil {
			t.Fatalf("user asset %s damaged by a normal uninstall: %v", d, err)
		}
	}
	if len(out.Purged) != 0 {
		t.Fatalf("normal run purged user data: %v", out.Purged)
	}
	if len(out.Deleted) == 0 || len(out.Failed) != 0 {
		t.Fatalf("deleted=%v failed=%v", out.Deleted, out.Failed)
	}
	keptKinds := map[string]bool{}
	for _, k := range out.Kept {
		keptKinds[k.Kind] = true
	}
	for _, kind := range []string{KindMemory, KindProject, KindSkill, KindWork} {
		if !keptKinds[kind] {
			t.Fatalf("asset kind %s missing from the kept report", kind)
		}
	}
	names := zipNames(t, backup)
	for _, want := range []string{"openpanda.db", "context/a.txt", "config.yaml"} {
		if !names[want] {
			t.Fatalf("backup zip missing %q (has %v)", want, names)
		}
	}
}

// TestExecuteUninstallPurgeRefusedKeepsAssets: when the user refuses the
// second (--purge) confirmation the CLI aborts before ExecuteUninstall or
// runs it without Purge — either way the invariant is that user assets are
// never deleted unless Purge is explicitly set.
func TestExecuteUninstallPurgeRefusedKeepsAssets(t *testing.T) {
	_, root, targets := newUninstallFixture(t)
	backup := filepath.Join(root, "backup.zip")

	out := ExecuteUninstall(targets, UninstallOptions{BackupPath: backup}) // Purge NOT confirmed

	for _, d := range assetDirs(root) {
		if _, err := os.Stat(filepath.Join(d, "KEEP.md")); err != nil {
			t.Fatalf("user asset %s deleted without a confirmed purge: %v", d, err)
		}
	}
	if len(out.Purged) != 0 {
		t.Fatalf("unconfirmed purge deleted assets: %v", out.Purged)
	}
	// The owned state may well be gone (first confirmation covered it) —
	// the contract here is only about the user assets.
}

func TestExecuteUninstallBackupOnly(t *testing.T) {
	home, root, targets := newUninstallFixture(t)
	db := filepath.Join(root, "data", "openpanda.db")
	ctxDir := filepath.Join(root, "data", "context")
	cfg := filepath.Join(home, ".openpanda", "config.yaml")
	backup := filepath.Join(root, "backup.zip")

	out := ExecuteUninstall(targets, UninstallOptions{BackupPath: backup, BackupOnly: true})

	if out.BackupErr != nil || out.BackupFiles != 3 {
		t.Fatalf("backup = %d files, err %v", out.BackupFiles, out.BackupErr)
	}
	for _, p := range []string{db, filepath.Join(ctxDir, "a.txt"), cfg} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s vanished under --backup-only: %v", p, err)
		}
	}
	for _, d := range assetDirs(root) {
		if _, err := os.Stat(filepath.Join(d, "KEEP.md")); err != nil {
			t.Fatalf("user asset %s vanished under --backup-only: %v", d, err)
		}
	}
	if len(out.Deleted) != 0 || len(out.Purged) != 0 || len(out.Failed) != 0 {
		t.Fatalf("--backup-only deleted something: deleted=%v purged=%v failed=%v", out.Deleted, out.Purged, out.Failed)
	}
	names := zipNames(t, backup)
	for _, want := range []string{"openpanda.db", "context/a.txt", "config.yaml"} {
		if !names[want] {
			t.Fatalf("backup zip missing %q (has %v)", want, names)
		}
	}
}

func TestExecuteUninstallPurge(t *testing.T) {
	home, root, targets := newUninstallFixture(t)
	db := filepath.Join(root, "data", "openpanda.db")
	cfg := filepath.Join(home, ".openpanda", "config.yaml")
	backup := filepath.Join(root, "backup.zip")

	out := ExecuteUninstall(targets, UninstallOptions{BackupPath: backup, Purge: true})

	for _, d := range assetDirs(root) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("user asset %s survived --purge", d)
		}
	}
	if len(out.Purged) != 4 {
		t.Fatalf("purged %d asset dirs, want 4 (%v)", len(out.Purged), out.Purged)
	}
	if len(out.Failed) != 0 {
		t.Fatalf("purge failures: %v", out.Failed)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("owned db survived --purge")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("owned config survived --purge")
	}
	// The dying assets must be recoverable from the backup: 3 owned files
	// + 4 KEEP.md files.
	if out.BackupErr != nil || out.BackupFiles != 7 {
		t.Fatalf("backup = %d files, err %v (want 7)", out.BackupFiles, out.BackupErr)
	}
	names := zipNames(t, backup)
	for _, want := range []string{"openpanda.db", "config.yaml", "memory/KEEP.md", "projects/KEEP.md", "skills/KEEP.md", "work/KEEP.md"} {
		if !names[want] {
			t.Fatalf("purge backup zip missing %q (has %v)", want, names)
		}
	}
}

// TestExecuteUninstallPurgeRefusesHome: an asset path that IS the home
// directory must never be purged, flag or not.
func TestExecuteUninstallPurgeRefusesHome(t *testing.T) {
	home := t.TempDir()
	setUserHome(t, home)
	// Pin the config to a custom (nonexistent) location so the test never
	// depends on — or touches — /etc/openpanda on the host.
	t.Setenv("OPENPANDA_CONFIG_PATH", filepath.Join(t.TempDir(), "none.yaml"))
	root := t.TempDir()
	db := filepath.Join(root, "openpanda.db")
	os.WriteFile(db, []byte("db"), 0o644)
	sentinel := filepath.Join(home, "precious.txt")
	os.WriteFile(sentinel, []byte("keep me"), 0o644)

	targets := Scan(PlanInput{
		Storage: &StoragePaths{
			DBPath:     db,
			MemoryPath: home, // pathological config: memory lives in $HOME
		},
		ConfigFileUsed: "",
		ExePath:        "",
		InstallDir:     "",
	})

	out := ExecuteUninstall(targets, UninstallOptions{Purge: true, BackupPath: filepath.Join(root, "backup.zip")})

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("purge deleted a file inside the home directory")
	}
	purgedHome := false
	for _, tg := range out.Purged {
		if tg.Path == filepath.Clean(home) {
			purgedHome = true
		}
	}
	if purgedHome {
		t.Fatal("home directory listed as purged")
	}
	keptHome := false
	for _, tg := range out.Kept {
		if tg.Path == filepath.Clean(home) && tg.Kind == KindMemory {
			keptHome = true
		}
	}
	if !keptHome {
		t.Fatalf("home-as-memory-asset missing from the kept report: %+v", out.Kept)
	}
}

func TestCopySelf(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, ExeName())
	if err := CopySelf(bin); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	// Windows never reports unix exec bits (mode is 0666 / read-only 0444);
	// executability there comes from the .exe extension ExeName() gave us.
	if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		t.Fatal("installed binary is not executable")
	}
	// Re-running the copy over an existing destination is the update path.
	if err := CopySelf(bin); err != nil {
		t.Fatalf("second copy over same path: %v", err)
	}
	// NOTE: Verify(bin) is deliberately not exercised here — under `go
	// test` os.Executable() is the test binary, so running it re-enters the
	// test suite. The real install flow (bin/panda install → smoke-tested
	// in a sandboxed HOME) covers it.
}
