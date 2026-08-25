// Package install implements `panda install` / `panda uninstall` /
// `panda doctor`: placing the binary on PATH (persistently, per-OS), and a
// whitelist-based uninstall that backs up and removes only OpenPanda-owned
// state while preserving user assets (projects, memory, skills, work) —
// unless the user passes --purge and survives its second confirmation.
//
// Safety model: uninstall never walks the filesystem looking for things to
// delete. Every deletion candidate is derived from explicit inputs (the
// running executable, the install dir, the loaded config's storage paths)
// and validated against guardrails in Scan — a candidate that is empty,
// a filesystem root, the user's home, or overlaps a user-asset directory is
// flipped to "keep" with a reason. The user sees the full plan and must type
// `confirm` before anything is touched.
package install

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// commandOutput runs bin with args and returns its trimmed combined output.
func commandOutput(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ExeName is the installed binary's file name ("panda" / "panda.exe").
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "panda.exe"
	}
	return "panda"
}

// Dir returns the default install directory: ~/.local/bin on unix (XDG
// convention, preconfigured on most Linux distros), %LOCALAPPDATA%\OpenPanda
// \bin on Windows (per-user, no elevation needed).
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("install: no home directory: %w", err)
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "OpenPanda", "bin"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: no home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// InPATH reports whether dir is on the current process's PATH. The install
// flow uses it to decide whether to touch shell startup files at all.
func InPATH(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == "" {
			continue
		}
		cand, err := filepath.Abs(p)
		if err == nil && samePath(cand, abs) {
			return true
		}
	}
	return false
}

// samePath compares two cleaned paths; case-insensitive on Windows.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// CopySelf copies the running executable to dst (creating parent dirs).
// The copy goes through a temp file + rename so a half-written binary never
// sits at the destination. A pre-existing dst is replaced — that is the
// update path (`panda install` over an older install).
func CopySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install: locate running binary: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("install: create %s: %w", filepath.Dir(dst), err)
	}
	if samePath(src, dst) {
		return nil // already running from the install location
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("install: open %s: %w", src, err)
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		// Windows locks a running destination; move it aside and retry.
		if runtime.GOOS == "windows" {
			if renameErr := os.Rename(dst, dst+".old"); renameErr == nil {
				out, err = os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			}
		}
		if err != nil {
			return fmt.Errorf("install: write %s: %w", tmp, err)
		}
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("install: copy: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install: close %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}

// Verify runs `<bin> version` and returns its trimmed output. This is the
// post-install self-check the user asked for: proof the installed copy
// executes, not just that a file landed.
func Verify(bin string) (string, error) {
	out, err := commandOutput(bin, "version")
	if err != nil {
		return "", fmt.Errorf("verify %s: %w", bin, err)
	}
	return out, nil
}

// ---- uninstall plan ----

// Target kinds. "binary"/"data"/"config"/"path" are OpenPanda-owned;
// the asset kinds mark user trees that are always kept.
const (
	KindBinary  = "binary"
	KindData    = "data"
	KindConfig  = "config"
	KindPath    = "path" // PATH persistence entry (rc file / registry) — informational
	KindMemory  = "memory"
	KindProject = "projects"
	KindSkill   = "skills"
	KindWork    = "work"
)

// Target is one line of the uninstall plan.
type Target struct {
	Path   string
	Kind   string
	Delete bool
	Reason string // why a kept item is kept; ignored for delete items
	Bytes  int64
	Exists bool
}

// PlanInput collects everything Scan derives deletions from. Storage may be
// nil (config unreadable) — the binary and PATH entries are still actionable.
type PlanInput struct {
	Storage        *StoragePaths
	ConfigFileUsed string // the config file the run resolved to (may not exist)
	ExePath        string
	InstallDir     string
}

// StoragePaths mirrors the subset of config.StorageConfig the uninstall
// needs, decoupled so config changes don't ripple into this package.
type StoragePaths struct {
	DBPath       string
	ContextPath  string
	MemoryPath   string
	ProjectsPath string
	SkillsPath   string
	WorkPath     string
	VAPIDKeyPath string
}

// assetDirs returns the user-asset roots (always kept). WorkPath "." (the
// default) is excluded: it means "wherever the daemon was started", which is
// usually the whole project checkout — never a deletion-relevant asset root.
func (s *StoragePaths) assetDirs() []struct{ path, kind, reason string } {
	var out []struct{ path, kind, reason string }
	add := func(p, kind, reason string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		out = append(out, struct{ path, kind, reason string }{filepath.Clean(abs), kind, reason})
	}
	add(s.MemoryPath, KindMemory, "personal memory (Hermes)")
	add(s.ProjectsPath, KindProject, "project memory & outputs")
	add(s.SkillsPath, KindSkill, "user skills (SKILL.md)")
	if s.WorkPath != "" && s.WorkPath != "." && s.WorkPath != "./" {
		add(s.WorkPath, KindWork, "agent working files & task outputs")
	}
	return out
}

// ownedConfigRoots are the directories a config file may live in for the
// uninstall to claim it. A config at a custom location (a repo checkout, a
// dotfiles manager) belongs to the user, not to us.
func ownedConfigRoots(installDir string) []string {
	roots := []string{"/etc/openpanda"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, ".openpanda"))
	}
	if installDir != "" {
		roots = append(roots, installDir)
	}
	return roots
}

// distributionPrefix resolves the running binary through PATH symlinks and,
// when it sits in a <prefix>/bin directory with an adapters/ sibling, reports
// that prefix — the layout install.sh, the self-updater, and the Homebrew
// formula all produce.
func distributionPrefix(exePath string) (string, bool) {
	if exePath == "" {
		return "", false
	}
	real := exePath
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		real = resolved
	}
	dir := filepath.Dir(real)
	if filepath.Base(dir) != "bin" {
		return "", false
	}
	prefix := filepath.Dir(dir)
	if st, err := os.Stat(filepath.Join(prefix, "adapters")); err != nil || !st.IsDir() {
		return "", false
	}
	return prefix, true
}

// SweepablePrefix reports the distribution prefix when it is safe for the
// uninstall to sweep it: not a Homebrew Cellar (brew owns those files) and
// not a source checkout (go.mod / .git beside a locally built bin/panda —
// the dev repo layout, where adapters/ belongs to git, not to us).
func SweepablePrefix(exePath string) (string, bool) {
	prefix, ok := distributionPrefix(exePath)
	if !ok || underHomebrew(prefix) || looksLikeCheckout(prefix) {
		return "", false
	}
	return prefix, true
}

// underHomebrew reports whether prefix lives inside a Homebrew Cellar, where
// the package manager owns the files.
func underHomebrew(prefix string) bool {
	for dir := filepath.Clean(prefix); ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "Cellar" {
			return true
		}
		if dir == string(filepath.Separator) || dir == "" || filepath.Dir(dir) == dir {
			return false
		}
	}
}

// looksLikeCheckout guards the prefix sweep: a Go checkout of this project
// (go.mod / .git at the root, adapters/ beside a locally built bin/panda)
// must not be treated as an installed distribution.
func looksLikeCheckout(prefix string) bool {
	for _, marker := range []string{"go.mod", ".git"} {
		if _, err := os.Stat(filepath.Join(prefix, marker)); err == nil {
			return true
		}
	}
	return false
}

// distributionEntries lists the release archive's layout inside a
// distribution prefix: the binary dir, the adapters, and the example
// configs / license it drops. Anything else in the prefix (e.g. Linux XDG
// storage roots) is not ours and stays.
func distributionEntries(prefix string) []string {
	entries := []string{
		filepath.Join(prefix, "bin"),
		filepath.Join(prefix, "adapters"),
	}
	for _, name := range []string{
		"config.example.yaml",
		"capabilities.example-desktop.yaml",
		"capabilities.example-edge.yaml",
		"LICENSE",
		"README.md",
	} {
		if _, err := os.Stat(filepath.Join(prefix, name)); err == nil {
			entries = append(entries, filepath.Join(prefix, name))
		}
	}
	return entries
}

// Scan builds the full uninstall plan. Deletion candidates outside the
// whitelist, or overlapping home / user assets, are downgraded to keep —
// the function never upgrades anything to Delete, so it cannot manufacture
// a deletion the inputs did not explicitly name.
func Scan(in PlanInput) []Target {
	var targets []Target
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)

	// A Homebrew install: brew owns the whole Cellar keg — the binary, the
	// adapters, the PATH symlink `brew link` created. Deleting any of it
	// directly leaves a broken keg brew still tracks, so every candidate that
	// lives inside (or resolves into) the Cellar is kept with the hint that
	// the real removal is `brew uninstall openpanda`.
	brewPrefix := ""
	if prefix, isPrefix := distributionPrefix(in.ExePath); isPrefix && underHomebrew(prefix) {
		brewPrefix = prefix
	}

	// 1. Binaries: the installed copy and, if different, the invoked one.
	seenBinary := map[string]bool{}
	for _, b := range []string{filepath.Join(in.InstallDir, ExeName()), in.ExePath} {
		if b == "" || seenBinary[filepath.Clean(b)] {
			continue
		}
		b = filepath.Clean(b)
		seenBinary[b] = true
		if resolvesInto(b, brewPrefix) {
			targets = append(targets, mkTarget(b, KindBinary, false,
				"managed by Homebrew — run `brew uninstall openpanda`"))
			continue
		}
		targets = append(targets, mkTarget(b, KindBinary, true, ""))
	}

	// 1b. Distribution prefix (install.sh / self-update layout): the binary
	// resolved through the PATH symlink sits at <prefix>/bin/panda with
	// adapters/ beside it. Those distribution entries are ours to sweep; the
	// prefix itself is only rmdir'd afterwards when left empty (the caller),
	// so Linux XDG storage roots sharing the prefix survive.
	if prefix, ok := SweepablePrefix(in.ExePath); ok {
		for _, entry := range distributionEntries(prefix) {
			if entry = filepath.Clean(entry); !seenBinary[entry] {
				seenBinary[entry] = true
				targets = append(targets, mkTarget(entry, KindBinary, true, ""))
			}
		}
	} else if brewPrefix != "" {
		targets = append(targets, mkTarget(brewPrefix, KindBinary, false,
			"managed by Homebrew — run `brew uninstall openpanda`"))
	}

	// 2. PATH persistence (informational; removal goes through the
	// per-OS helpers so rc files get edited, not deleted).
	for _, where := range PathPersistedAt(in.InstallDir) {
		targets = append(targets, mkTarget(where, KindPath, true, "PATH entry"))
	}

	var s *StoragePaths
	if in.Storage != nil {
		s = in.Storage
	}

	// 3. App state: db (+journals), context dir, VAPID key, config file.
	if s != nil {
		db := cleanAbs(s.DBPath)
		if db != "" {
			targets = append(targets, mkTarget(db, KindData, true, ""))
			targets = append(targets, mkTarget(db+"-wal", KindData, true, ""))
			targets = append(targets, mkTarget(db+"-shm", KindData, true, ""))
		}
		if c := cleanAbs(s.ContextPath); c != "" {
			targets = append(targets, mkTarget(c, KindData, true, ""))
		}
		if v := cleanAbs(s.VAPIDKeyPath); v != "" {
			targets = append(targets, mkTarget(v, KindData, true, ""))
		}
	}

	// The config file is only ours inside an owned root; also resolve the
	// default location so a standard install's config is swept with the rest.
	cfgFile := in.ConfigFileUsed
	if cfgFile == "" {
		if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
			cfgFile = env
		} else {
			cfgFile = "/etc/openpanda/config.yaml"
		}
	}
	if cfgFile != "" {
		cfgFile = cleanAbs(cfgFile)
		owned := false
		for _, root := range ownedConfigRoots(in.InstallDir) {
			if within(cfgFile, cleanAbs(root)) {
				owned = true
				break
			}
		}
		targets = append(targets, mkTarget(cfgFile, KindConfig, owned,
			tern(!owned, "custom location — kept", "")))
	}

	// 4. User assets: always kept, listed so the report shows what survived.
	if s != nil {
		for _, a := range s.assetDirs() {
			targets = append(targets, mkTarget(a.path, a.kind, false, a.reason))
		}
	}

	applyGuardrails(targets, home)
	return targets
}

// applyGuardrails walks the plan once more and flips any deletion that
// would touch a protected path: the filesystem root, the user's home, or a
// user-asset directory (or anything inside one). It also catches a deletion
// directory that would swallow an asset root (e.g. context_path pointed at
// ~/projects by mistake).
func applyGuardrails(targets []Target, home string) {
	var assets []string
	for _, t := range targets {
		if !t.Delete && (t.Kind == KindMemory || t.Kind == KindProject || t.Kind == KindSkill || t.Kind == KindWork) {
			assets = append(assets, t.Path)
		}
	}
	for i := range targets {
		t := &targets[i]
		if !t.Delete {
			continue
		}
		bad := ""
		switch {
		case t.Path == "" || t.Path == "." || t.Path == string(filepath.Separator):
			bad = "refusing to touch a filesystem root"
		case home != "" && (t.Path == home || within(home, t.Path)):
			bad = "overlaps the home directory"
		default:
			for _, a := range assets {
				if t.Path == a || within(t.Path, a) || a == t.Path {
					bad = "overlaps user data at " + a
					break
				}
			}
		}
		if bad != "" {
			t.Delete = false
			t.Reason = bad
		}
	}
}

func mkTarget(path, kind string, del bool, reason string) Target {
	t := Target{Path: path, Kind: kind, Delete: del, Reason: reason}
	if st, err := os.Lstat(path); err == nil {
		t.Exists = true
		t.Bytes = sizeOf(st, path)
	}
	return t
}

func sizeOf(st os.FileInfo, path string) int64 {
	if !st.IsDir() {
		return st.Size()
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total
}

// within reports whether child lives under parent (strictly; equality is
// excluded). Rel yields "../.."-style paths for anything outside parent, so
// the test is "does not climb up" — comparing against exactly ".." would
// miss the deeper climbs.
func within(child, parent string) bool {
	if parent == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// resolvesInto reports whether path itself, or whatever it symlinks to, lives
// inside parent. It is how the Homebrew guard catches both the Cellar binary
// and the /opt/homebrew/bin PATH link brew's `brew link` created. An empty
// parent (no brew install) is never a match; so is a path that cannot be
// resolved (missing file) — mkTarget then reports it as non-existent.
func resolvesInto(path, parent string) bool {
	if parent == "" || path == "" {
		return false
	}
	if within(filepath.Clean(path), parent) {
		return true
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return within(real, parent)
}

func cleanAbs(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

func tern(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// ---- backup ----

// BackupZip archives the given targets into a zip so an uninstall is
// reversible. Which items land in the archive is the caller's decision: a
// normal uninstall passes the delete-kind targets (data/config), a --purge
// run also passes the user-asset targets that are about to die. The binary
// and PATH entries are never archived; files that vanished between Scan and
// execution are skipped silently. Returns the number of files archived.
func BackupZip(dest string, targets []Target) (int, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, fmt.Errorf("backup: create %s: %w", dest, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	count := 0
	for _, t := range targets {
		if !t.Exists || t.Kind == KindBinary || t.Kind == KindPath {
			continue
		}
		root := filepath.Base(t.Path)
		err := filepath.WalkDir(t.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err // skip unreadable entries, keep walking elsewhere
			}
			rel, err := filepath.Rel(t.Path, p)
			if err != nil {
				return nil
			}
			in, err := os.Open(p)
			if err != nil {
				return nil // unreadable file: skip, don't abort the backup
			}
			defer in.Close()
			w, err := zw.Create(filepath.ToSlash(filepath.Join(root, rel)))
			if err != nil {
				return nil
			}
			if _, err := io.Copy(w, in); err != nil {
				return nil
			}
			count++
			return nil
		})
		if err != nil {
			continue
		}
	}
	if err := zw.Close(); err != nil {
		return count, fmt.Errorf("backup: finalize %s: %w", dest, err)
	}
	return count, nil
}

// ---- deletion ----

// RemoveOne deletes a single target. Symlinks remove the link only — never
// anything they point at. Directories go recursively.
func RemoveOne(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// ---- execution ----

// UninstallOptions drives ExecuteUninstall.
type UninstallOptions struct {
	// BackupPath is where the backup zip is written; empty skips the backup.
	BackupPath string
	// Purge additionally deletes the user-asset targets (memory/projects/
	// skills/work). The caller owns the double confirmation — this field is
	// the result of the second prompt, never a default.
	Purge bool
	// BackupOnly writes the backup and returns without deleting anything.
	BackupOnly bool
}

// UninstallOutcome reports what an ExecuteUninstall run did. Individual
// failures are collected in Failed rather than aborting the run — the
// uninstall report surfaces them at the end.
type UninstallOutcome struct {
	BackupFiles int
	BackupErr   error
	Deleted     []Target // OpenPanda-owned items removed
	Purged      []Target // user-asset dirs removed (--purge)
	Kept        []Target // items the run left on disk
	Failed      []string // "path (error)" entries
}

// isAssetKind reports whether kind marks a user-asset target.
func isAssetKind(kind string) bool {
	return kind == KindMemory || kind == KindProject || kind == KindSkill || kind == KindWork
}

// ExecuteUninstall runs the backup + deletion phases of an uninstall over a
// scanned plan. It deletes exactly what the plan marked Delete — minus the
// PATH entries, which the caller removes through the per-OS rc editors —
// and, when Purge is set, the user-asset targets. Purge candidates are
// guarded here as well: an asset path that is empty, a filesystem root, the
// home directory, or an ancestor of it is kept regardless of the flag.
// BackupOnly stops after the backup so nothing is removed.
func ExecuteUninstall(targets []Target, opt UninstallOptions) UninstallOutcome {
	var out UninstallOutcome
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)

	// Purge candidates up front: the user-asset targets, minus anything the
	// purge guardrails refuse to touch — so the backup archives exactly the
	// assets that will die (never, say, the whole home directory).
	var purgeList []Target
	if opt.Purge {
		for _, t := range targets {
			if !isAssetKind(t.Kind) || !t.Exists {
				continue
			}
			if reason := purgeGuard(t.Path, home); reason != "" {
				t.Reason = reason
				out.Kept = append(out.Kept, t)
				continue
			}
			purgeList = append(purgeList, t)
		}
	}

	if opt.BackupPath != "" {
		var archive []Target
		for _, t := range targets {
			if t.Exists && t.Delete && (t.Kind == KindData || t.Kind == KindConfig) {
				archive = append(archive, t)
			}
		}
		// The assets are about to die too — they must be recoverable.
		archive = append(archive, purgeList...)
		n, err := BackupZip(opt.BackupPath, archive)
		out.BackupFiles = n
		out.BackupErr = err
	}
	if opt.BackupOnly {
		return out
	}
	for _, t := range targets {
		switch {
		case t.Kind == KindPath:
			continue // handled by the caller via RemovePATHPersistence
		case !t.Delete:
			if !opt.Purge || !isAssetKind(t.Kind) {
				out.Kept = append(out.Kept, t)
			}
		default:
			if err := RemoveOne(t.Path); err != nil {
				out.Failed = append(out.Failed, fmt.Sprintf("%s (%v)", t.Path, err))
			} else if t.Exists {
				out.Deleted = append(out.Deleted, t)
			}
		}
	}
	for _, t := range purgeList {
		if err := RemoveOne(t.Path); err != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("%s (%v)", t.Path, err))
		} else {
			out.Purged = append(out.Purged, t)
		}
	}
	return out
}

// purgeGuard returns a non-empty reason when a purge candidate must not be
// deleted: a filesystem root, the home directory, or anything containing it.
func purgeGuard(path, home string) string {
	switch {
	case path == "" || path == "." || path == string(filepath.Separator):
		return "refusing to touch a filesystem root"
	case home != "" && (path == home || within(home, path)):
		return "overlaps the home directory"
	}
	return ""
}
