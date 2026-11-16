// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReviewedSkillsRetainProvenanceAndResources(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, name := range []string{"frontend-design", "systematic-debugging", "verification-before-completion", "writing-guidelines"} {
		t.Run(name, func(t *testing.T) {
			sk, err := InstallFromHub(context.Background(), store, "", name, ImportOptions{Status: StatusPending})
			if err != nil {
				t.Fatal(err)
			}
			if !sk.Builtin || sk.Always || sk.Provenance == nil || len(sk.Provenance.Revision) != 40 {
				t.Fatalf("invalid reviewed metadata: %#v", sk)
			}
			if _, err := store.Read(Access{}, ReadRequest{Name: name}); err == nil {
				t.Fatal("pending skill visible to an agent")
			}
			if err := store.Approve(ScopeGlobal, "", name); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"LICENSE.txt", "SOURCE.md", "upstream-SKILL.md"} {
				result, err := store.Read(Access{}, ReadRequest{Name: name, Path: path, Limit: 16000})
				if err != nil || result.Content == "" {
					t.Fatalf("%s: %v", path, err)
				}
				if path == "upstream-SKILL.md" {
					if err := verifyDownload([]byte(result.Content), sk.Provenance.SHA256); err != nil {
						t.Fatal(err)
					}
				}
			}
			loaded, err := store.Load(ScopeGlobal, "", name)
			if err != nil || loaded.Provenance == nil || *loaded.Provenance != *sk.Provenance {
				t.Fatalf("provenance not persisted: %#v, %v", loaded, err)
			}
		})
	}
	result, err := store.Read(Access{}, ReadRequest{Name: "writing-guidelines", Path: "guidelines.md"})
	if err != nil || !result.Truncated {
		t.Fatalf("expected paginated guidelines: %#v, %v", result, err)
	}
	next, err := store.Read(Access{}, ReadRequest{Name: "writing-guidelines", Path: "guidelines.md", Offset: result.NextOffset, SHA256: result.SHA256})
	if err != nil || next.Offset != result.NextOffset {
		t.Fatalf("pagination failed: %#v, %v", next, err)
	}
	matches, err := store.Search(Access{}, "翻译文案", 5)
	if err != nil || len(matches) == 0 || matches[0].Name != "writing-guidelines" {
		t.Fatalf("bilingual discovery: %#v, %v", matches, err)
	}
}

func TestDiscoverUsesSelectedCatalogAndScope(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(HubIndex{Skills: []HubSkill{{Name: "scoped-helper", Description: "scoped-helper", Content: strings.Join([]string{"---", "name: wrong-package-name", "description: scoped-helper", "---", "Scope test"}, string(rune(10)))}}})
	}))
	defer ts.Close()
	store := NewStore(t.TempDir())
	if err := store.Save(&Skill{Name: "scoped-helper", Description: "existing global", Scope: ScopeGlobal, Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	sk, fresh, err := store.DiscoverAndInstall(context.Background(), ts.URL, "scoped-helper", ImportOptions{Scope: ScopeProject, Project: "demo", Status: StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if !fresh || sk.Name != "scoped-helper" || sk.Scope != ScopeProject || sk.Project != "demo" || sk.Status != StatusPending {
		t.Fatalf("wrong scoped result: %#v fresh=%v", sk, fresh)
	}
	if calls.Load() != 1 {
		t.Fatalf("catalog refetched after selection: %d", calls.Load())
	}
}

// Hashes pin the reviewed bytes, including licenses. Windows Git normally
// converts unclassified text files to CRLF; exercise an actual checkout so a
// missing attribute cannot make every binary panic during package init again.
func TestReviewedResourcesKeepPinnedBytesInWindowsCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	attrs, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0600); err != nil {
		t.Fatal(err)
	}
	originals := make(map[string][]byte)
	if err := fs.WalkDir(reviewedFiles, "reviewed", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := reviewedFiles.ReadFile(name)
		if err != nil {
			return err
		}
		rel := filepath.Join("internal", "skills", filepath.FromSlash(name))
		file := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			return err
		}
		originals[rel] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		args = append([]string{"-c", "core.autocrlf=true", "-c", "core.safecrlf=false"}, args...)
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "--", ".gitattributes", "internal/skills/reviewed")
	checkout := t.TempDir()
	run("checkout-index", "--all", "--prefix="+filepath.ToSlash(checkout)+"/")
	for rel, want := range originals {
		got, err := os.ReadFile(filepath.Join(checkout, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Windows checkout changed reviewed bytes: %s", rel)
		}
	}
}
