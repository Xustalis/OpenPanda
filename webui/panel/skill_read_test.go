// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/skills"
)

func TestSkillPreviewIsScopedAndDoesNotActivate(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	for _, project := range []string{"one", "two"} {
		if err := store.Save(&skills.Skill{Name: "same", Description: "preview", Scope: skills.ScopeProject, Project: project, Status: skills.StatusPending, Body: project + " procedure"}); err != nil {
			t.Fatal(err)
		}
	}
	h := &handler{skillStore: store}
	q := url.Values{"name": {"same"}, "scope": {"project"}, "key": {"one"}}
	w := httptest.NewRecorder()
	h.readSkill(w, httptest.NewRequest("GET", "/api/skills/read?"+q.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var read skills.ReadResult
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || !strings.Contains(read.Content, "one procedure") {
		t.Fatalf("wrong scoped preview: %+v %v", read, err)
	}
	sk, _ := store.Load(skills.ScopeProject, "one", "same")
	if sk.Status != skills.StatusPending || sk.UseCount != 0 {
		t.Fatal("preview activated or used skill")
	}
	req := httptest.NewRequest("POST", "/api/skills/approve", strings.NewReader(`{"name":"same","scope":"project","key":"one"}`))
	w = httptest.NewRecorder()
	h.skillAction(true)(w, req)
	if w.Code != 200 {
		t.Fatalf("scoped approval failed: %d %s", w.Code, w.Body.String())
	}
	sk, _ = store.Load(skills.ScopeProject, "two", "same")
	if sk.Status != skills.StatusPending {
		t.Fatal("approved unrelated same-name skill")
	}
}
