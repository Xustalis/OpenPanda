package install

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestAddRemovePATHIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")

	rc := filepath.Join(home, ".zshrc")
	orig := "# user content\nexport EDITOR=vim\n"
	if err := os.WriteFile(rc, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".local", "bin")
	written, err := AddToPATH(dir)
	if err != nil || len(written) != 1 || written[0] != rc {
		t.Fatalf("AddToPATH = %v, %v", written, err)
	}
	data, _ := os.ReadFile(rc)
	s := string(data)
	if !strings.Contains(s, markerBegin) || !strings.Contains(s, dir) {
		t.Fatalf("rc missing marker/export: %q", s)
	}
	if !strings.Contains(s, "export EDITOR=vim") {
		t.Fatal("user content lost")
	}

	// Second run must not duplicate the block.
	if _, err := AddToPATH(dir); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(rc)
	if got := strings.Count(string(data), markerBegin); got != 1 {
		t.Fatalf("marker duplicated %d times", got)
	}

	// Doctor's persistence probe must see it; removal must restore the file.
	if got := PathPersistedAt(dir); len(got) != 1 {
		t.Fatalf("PathPersistedAt = %v, want [%s]", got, rc)
	}
	changed, err := RemovePATHPersistence(dir)
	if err != nil || len(changed) != 1 {
		t.Fatalf("RemovePATHPersistence = %v, %v", changed, err)
	}
	data, _ = os.ReadFile(rc)
	if strings.Contains(string(data), markerBegin) {
		t.Fatalf("marker survived removal: %q", data)
	}
	if !strings.Contains(string(data), "export EDITOR=vim") {
		t.Fatal("user content lost on removal")
	}
}

func TestScanGuardrails(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")

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
	os.Symlink(target, link)

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
	if st.Mode()&0o111 == 0 {
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
