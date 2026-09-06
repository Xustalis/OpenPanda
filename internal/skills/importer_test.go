package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func sampleSkillBytes() []byte {
	return []byte(`---
name: custom-test-skill
description: A skill created for test verification
scope: global
status: active
use_count: 0
success_count: 0
---

## Steps
1. Echo hello world
2. Check result
`)
}

func TestImportBytes(t *testing.T) {
	store := NewStore(t.TempDir())
	opts := ImportOptions{}

	sk, err := store.ImportBytes(sampleSkillBytes(), opts)
	if err != nil {
		t.Fatalf("ImportBytes failed: %v", err)
	}
	if sk.Name != "custom-test-skill" || sk.Status != StatusActive {
		t.Errorf("unexpected skill: %+v", sk)
	}

	// Verify it was persisted on disk
	loaded, err := store.Load(ScopeGlobal, "", "custom-test-skill")
	if err != nil || loaded == nil {
		t.Fatalf("failed to load saved skill: %v", err)
	}
	if loaded.Description != "A skill created for test verification" {
		t.Errorf("description mismatch: %s", loaded.Description)
	}

	// Duplicate without force should error
	_, err = store.ImportBytes(sampleSkillBytes(), opts)
	if err == nil {
		t.Errorf("expected error importing duplicate skill without force")
	}

	// Duplicate with force should succeed
	opts.Force = true
	_, err = store.ImportBytes(sampleSkillBytes(), opts)
	if err != nil {
		t.Errorf("expected success with force=true: %v", err)
	}
}

func TestImportBytesScopeAndNameOverrides(t *testing.T) {
	store := NewStore(t.TempDir())
	opts := ImportOptions{
		Name:    "renamed-skill",
		Scope:   ScopeProject,
		Project: "my-proj",
		Status:  StatusPending,
	}

	sk, err := store.ImportBytes(sampleSkillBytes(), opts)
	if err != nil {
		t.Fatalf("ImportBytes with overrides failed: %v", err)
	}
	if sk.Name != "renamed-skill" || sk.Scope != ScopeProject || sk.Project != "my-proj" || sk.Status != StatusPending {
		t.Errorf("overrides not applied: %+v", sk)
	}

	loaded, err := store.Load(ScopeProject, "my-proj", "renamed-skill")
	if err != nil || loaded == nil {
		t.Fatalf("could not load project skill: %v", err)
	}
}

func TestImportFileSingle(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "my-skill.md")
	if err := os.WriteFile(filePath, sampleSkillBytes(), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	store := NewStore(t.TempDir())
	skills, err := store.ImportFile(filePath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile single failed: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "custom-test-skill" {
		t.Errorf("unexpected skills: %v", skills)
	}
}

func TestImportFileDirectory(t *testing.T) {
	tmp := t.TempDir()
	skill1Dir := filepath.Join(tmp, "skill1")
	skill2Dir := filepath.Join(tmp, "sub", "skill2")
	os.MkdirAll(skill1Dir, 0o755)
	os.MkdirAll(skill2Dir, 0o755)

	content1 := `---
name: dir-skill-1
description: First dir skill
scope: global
---
Body 1`
	content2 := `---
name: dir-skill-2
description: Second dir skill
scope: global
---
Body 2`

	os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(content1), 0o644)
	os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(content2), 0o644)

	store := NewStore(t.TempDir())
	skills, err := store.ImportFile(tmp, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile dir failed: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
}

func TestImportZipArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("skills/archived-skill/SKILL.md")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	skillData := `---
name: archived-skill
description: Skill inside zip
scope: global
---
Zip body`
	if _, err := w.Write([]byte(skillData)); err != nil {
		t.Fatalf("write zip data: %v", err)
	}
	zw.Close()

	zipPath := filepath.Join(t.TempDir(), "skills.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip file: %v", err)
	}

	store := NewStore(t.TempDir())
	skills, err := store.ImportFile(zipPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile zip failed: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "archived-skill" {
		t.Errorf("unexpected zip import result: %v", skills)
	}
}

func TestImportTarGzArchive(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	data := []byte(`---
name: tar-skill
description: Skill inside tar.gz
scope: global
---
Tar body`)

	hdr := &tar.Header{
		Name: "tar-skill/SKILL.md",
		Mode: 0o644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	tw.Close()
	gw.Close()

	tarPath := filepath.Join(t.TempDir(), "skills.tar.gz")
	if err := os.WriteFile(tarPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar.gz file: %v", err)
	}

	store := NewStore(t.TempDir())
	skills, err := store.ImportFile(tarPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile tar.gz failed: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "tar-skill" {
		t.Errorf("unexpected tar import result: %v", skills)
	}
}

func TestNormalizeURL(t *testing.T) {
	in := "https://github.com/owner/repo/blob/main/skills/test/SKILL.md"
	want := "https://raw.githubusercontent.com/owner/repo/main/skills/test/SKILL.md"
	got := NormalizeURL(in)
	if got != want {
		t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
	}

	normal := "https://example.com/skills/test.md"
	if NormalizeURL(normal) != normal {
		t.Errorf("NormalizeURL changed normal URL: %s", NormalizeURL(normal))
	}
}

func TestImportURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		w.Write(sampleSkillBytes())
	}))
	defer ts.Close()

	store := NewStore(t.TempDir())
	skills, err := store.ImportURL(context.Background(), ts.URL+"/SKILL.md", ImportOptions{})
	if err != nil {
		t.Fatalf("ImportURL failed: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "custom-test-skill" {
		t.Errorf("unexpected imported skill: %v", skills)
	}
}
