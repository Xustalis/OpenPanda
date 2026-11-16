// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
