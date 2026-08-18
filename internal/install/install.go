// Package install implements `panda install` / `panda uninstall` /
// `panda doctor`: placing the binary on PATH (persistently, per-OS), and a
// whitelist-based uninstall that backs up and removes only OpenPanda-owned
// state while preserving user assets (projects, memory, skills, work).
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

// Scan builds the full uninstall plan. Deletion candidates outside the
// whitelist, or overlapping home / user assets, are downgraded to keep —
// the function never upgrades anything to Delete, so it cannot manufacture
// a deletion the inputs did not explicitly name.
func Scan(in PlanInput) []Target {
	var targets []Target
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)

	// 1. Binaries: the installed copy and, if different, the invoked one.
	seenBinary := map[string]bool{}
	for _, b := range []string{filepath.Join(in.InstallDir, ExeName()), in.ExePath} {
		if b == "" || seenBinary[filepath.Clean(b)] {
			continue
		}
		b = filepath.Clean(b)
		seenBinary[b] = true
		targets = append(targets, mkTarget(b, KindBinary, true, ""))
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

// BackupZip writes the existing delete-kind targets (data/config — never the
// binary, never rc entries) into a zip so an uninstall is reversible. Files
// that vanished between Scan and execution are skipped silently. Returns the
// number of files archived.
func BackupZip(dest string, targets []Target) (int, error) {
	f, err := os.Create(dest)
	if err != nil {
		return 0, fmt.Errorf("backup: create %s: %w", dest, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	count := 0
	for _, t := range targets {
		if !t.Delete || !t.Exists || (t.Kind != KindData && t.Kind != KindConfig) {
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
