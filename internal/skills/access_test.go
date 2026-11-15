// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBundleResourcesAndProgressiveRead(t *testing.T) {
	source := t.TempDir()
	_ = os.MkdirAll(filepath.Join(source, "references"), 0o700)
	_ = os.MkdirAll(filepath.Join(source, "scripts"), 0o700)
	_ = os.WriteFile(filepath.Join(source, "SKILL.md"), sampleSkillBytes(), 0o600)
	reference := strings.Repeat("分阶段读取资源；", 200)
	_ = os.WriteFile(filepath.Join(source, "references", "guide.md"), []byte(reference), 0o600)
	_ = os.WriteFile(filepath.Join(source, "scripts", "check.py"), []byte("print('checked')\n"), 0o600)
	store := NewStore(t.TempDir())
	if _, err := store.ImportFile(source, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Read(Access{}, ReadRequest{Name: "custom-test-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Resources) != 2 || !strings.Contains(first.Content, "Echo hello world") {
		t.Fatalf("missing bundle content: %+v", first)
	}
	offset := 0
	var content strings.Builder
	for {
		r, err := store.Read(Access{}, ReadRequest{Name: first.Name, Path: "references/guide.md", Offset: offset, Limit: 67})
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(r.Content) {
			t.Fatal("split UTF-8")
		}
		content.WriteString(r.Content)
		if !r.Truncated {
			break
		}
		if r.NextOffset <= offset {
			t.Fatal("paging did not advance")
		}
		offset = r.NextOffset
	}
	if content.String() != reference {
		t.Fatal("paged content changed")
	}
	if _, err := store.Read(Access{}, ReadRequest{Name: first.Name, Path: "references/guide.md", Offset: 67, SHA256: "stale"}); err == nil {
		t.Fatal("changed resource was not detected before returning another page")
	}
	if err := store.RecordUse(ScopeGlobal, "", first.Name, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(first.Root, "scripts", "check.py"))
	if err != nil || !strings.Contains(string(script), "checked") {
		t.Fatalf("metadata update discarded script: %v", err)
	}
	// Force imports replace a complete bundle, including removing stale files.
	_ = os.Remove(filepath.Join(source, "scripts", "check.py"))
	if _, err := store.ImportFile(source, ImportOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "scripts", "check.py")); !os.IsNotExist(err) {
		t.Fatalf("stale script survived replacement: %v", err)
	}
	if _, err := store.ImportBytes(sampleSkillBytes(), ImportOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "references", "guide.md")); !os.IsNotExist(err) {
		t.Fatalf("standalone replacement retained stale resources: %v", err)
	}
}

func TestSkillAccessScopesApprovalAndAmbiguity(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, sk := range []*Skill{
		{Name: "same", Description: "test", Scope: ScopeGlobal, Status: StatusActive, Body: "global"},
		{Name: "same", Description: "test", Scope: ScopeProject, Project: "one", Status: StatusActive, Body: "project"},
		{Name: "pending", Description: "test", Scope: ScopeGlobal, Status: StatusPending, Body: "unapproved"},
		{Name: "foreign", Description: "test", Scope: ScopeDevice, Device: "other", Status: StatusActive, Body: "private"},
	} {
		if err := s.Save(sk); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"pending", "foreign"} {
		if _, err := s.Read(Access{}, ReadRequest{Name: name}); err == nil {
			t.Fatalf("read unavailable %s", name)
		}
	}
	if _, err := s.Read(Access{Project: "one"}, ReadRequest{Name: "same"}); err == nil {
		t.Fatal("ambiguous skill silently resolved")
	}
	r, err := s.Read(Access{Project: "one"}, ReadRequest{Name: "same", Scope: ScopeProject, Key: "one"})
	if err != nil || !strings.Contains(r.Content, "project") {
		t.Fatalf("scoped read: %+v %v", r, err)
	}
	list, err := s.Search(Access{}, "", 1)
	if err != nil || len(list) != 1 || list[0].Name != "same" {
		t.Fatalf("bounded scoped search: %+v %v", list, err)
	}
}

func TestSkillResourceTraversalAndSymlink(t *testing.T) {
	s := NewStore(t.TempDir())
	sk := &Skill{Name: "safe", Description: "safe", Scope: ScopeGlobal, Status: StatusActive, Body: "ok"}
	if err := s.Save(sk); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	p, _ := s.Path(sk)
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(p), "escape")); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"../secret", "/etc/passwd", "a/../../secret", `a\b`, "C:secret", "escape"} {
		if r, err := s.Read(Access{}, ReadRequest{Name: "safe", Path: resource}); err == nil {
			t.Fatalf("unsafe read %q: %+v", resource, r)
		}
	}
}

func TestArchivesPreserveResourcesAndRejectUnsafeEntries(t *testing.T) {
	for _, format := range []string{"zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			for _, unsafe := range []string{"", "../outside", "/absolute", "a/../../outside", `a\b`, "C:/outside"} {
				t.Run(fmt.Sprintf("path=%s", unsafe), func(t *testing.T) {
					files := map[string][]byte{"bundle/SKILL.md": sampleSkillBytes(), "bundle/scripts/run.py": []byte("print(1)"), "bundle/references/guide.md": []byte("guide")}
					if unsafe != "" {
						files[unsafe] = []byte("bad")
					}
					s := NewStore(t.TempDir())
					var buf bytes.Buffer
					var imported []*Skill
					var err error
					if format == "zip" {
						z := zip.NewWriter(&buf)
						for name, data := range files {
							w, _ := z.Create(name)
							_, _ = w.Write(data)
						}
						_ = z.Close()
						imported, err = s.importFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), ImportOptions{})
					} else {
						gz := gzip.NewWriter(&buf)
						tr := tar.NewWriter(gz)
						for name, data := range files {
							_ = tr.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o600, Size: int64(len(data))})
							_, _ = tr.Write(data)
						}
						_ = tr.Close()
						_ = gz.Close()
						imported, err = s.importFromTarGz(bytes.NewReader(buf.Bytes()), ImportOptions{})
					}
					if unsafe != "" {
						if err == nil {
							t.Fatal("unsafe archive accepted")
						}
						idx, _ := s.Index()
						if len(idx) != 0 {
							t.Fatal("invalid archive partly installed")
						}
						return
					}
					if err != nil || len(imported) != 1 {
						t.Fatalf("import: %v %v", imported, err)
					}
					r, err := s.Read(Access{}, ReadRequest{Name: imported[0].Name, Path: "scripts/run.py"})
					if err != nil || r.Content != "print(1)" {
						t.Fatalf("resource: %+v %v", r, err)
					}
				})
			}
		})
	}
}

func TestBundleRejectsAggregateSizeAndFileCount(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.ImportBytes(make([]byte, maxDownloadBytes+1), ImportOptions{}); err == nil {
		t.Fatal("standalone import bypassed the size limit")
	}
	files := bundleFiles{}
	if err := files.add("first", bytes.NewReader(make([]byte, maxDownloadBytes/2+1))); err != nil {
		t.Fatal(err)
	}
	if err := files.add("second", bytes.NewReader(make([]byte, maxDownloadBytes/2))); err == nil {
		t.Fatal("accepted expanded bundle over the aggregate byte limit")
	}
	files = bundleFiles{}
	for i := 0; i < maxBundleFiles; i++ {
		if err := files.add(fmt.Sprintf("file-%d", i), strings.NewReader("")); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.add("one-too-many", strings.NewReader("")); err == nil {
		t.Fatal("accepted too many bundle files")
	}
}

func TestArchivesRejectLinksWithoutInstalling(t *testing.T) {
	for _, format := range []string{"zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			store := NewStore(t.TempDir())
			var err error
			if format == "zip" {
				z := zip.NewWriter(&buf)
				w, _ := z.Create("bundle/SKILL.md")
				_, _ = w.Write(sampleSkillBytes())
				h := &zip.FileHeader{Name: "bundle/scripts/link"}
				h.SetMode(os.ModeSymlink | 0o777)
				w, _ = z.CreateHeader(h)
				_, _ = w.Write([]byte("/outside"))
				_ = z.Close()
				_, err = store.importFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), ImportOptions{})
			} else {
				gz := gzip.NewWriter(&buf)
				tr := tar.NewWriter(gz)
				data := sampleSkillBytes()
				_ = tr.WriteHeader(&tar.Header{Name: "bundle/SKILL.md", Mode: 0o600, Size: int64(len(data))})
				_, _ = tr.Write(data)
				_ = tr.WriteHeader(&tar.Header{Name: "bundle/scripts/link", Typeflag: tar.TypeSymlink, Linkname: "/outside"})
				_ = tr.Close()
				_ = gz.Close()
				_, err = store.importFromTarGz(bytes.NewReader(buf.Bytes()), ImportOptions{})
			}
			if err == nil {
				t.Fatal("accepted archive link")
			}
			index, err := store.Index()
			if err != nil || len(index) != 0 {
				t.Fatalf("invalid archive partly installed: %+v %v", index, err)
			}
		})
	}
}
