// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// graphNodeIDs flattens a graph response into a set for membership checks.
func graphNodeIDs(t *testing.T, out map[string]any) map[string]bool {
	t.Helper()
	raw, ok := out["nodes"].([]any)
	if !ok {
		t.Fatalf("nodes missing or wrong type: %T", out["nodes"])
	}
	ids := map[string]bool{}
	for _, n := range raw {
		m, ok := n.(map[string]any)
		if !ok {
			t.Fatalf("node entry wrong type: %T", n)
		}
		ids[m["id"].(string)] = true
	}
	return ids
}

func graphHasEdge(out map[string]any, from, to, kind string) bool {
	return graphEdgeWeight(out, from, to, kind) > 0
}

// graphEdgeWeight returns the edge's weight, 0 when the edge is absent (the
// backend only emits positive weights, so existence and weight coincide).
func graphEdgeWeight(out map[string]any, from, to, kind string) int {
	raw, _ := out["edges"].([]any)
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["from"] == from && m["to"] == to && m["kind"] == kind {
			w, _ := m["weight"].(float64)
			return int(w)
		}
	}
	return 0
}

// A fresh node still answers 200 with the core surfaces — sparse, not broken.
func TestMemoryGraphEmptyMemory(t *testing.T) {
	h, _ := newMemoryPanel(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := graphNodeIDs(t, out)
	for _, want := range []string{"core:user", "core:memory", "core:dreams", "daily"} {
		if !ids[want] {
			t.Errorf("missing core node %s in %+v", want, ids)
		}
	}
}

// Promotion tags and literal name mentions must become edges — the two
// honest edge kinds the view promises.
func TestMemoryGraphEdges(t *testing.T) {
	_, cfg := newMemoryPanel(t)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memory.Limits{})

	// MEMORY.md carries dreamer-promoted entries; one names a topic twice and
	// one points back at USER.md (a core-file mention is a reference edge too).
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{
		"team ships fridays " + memory.PromotionSourceTag,
		"deploys moved " + memory.PromotionSourceTag,
		"see work and the work calendar",
		"profile fields live in USER.md",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("work", memory.MemFile{Entries: []string{"friday deploys"}}); err != nil {
		t.Fatal(err)
	}
	// A daily file gives the diary node real mass.
	dailyDir := hermes.WarmDir()
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dailyDir, "2026-01-02.md"), []byte("# diary\nshipped a thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A pending skill renders dimmed; status must round-trip. Rebuild the
	// panel with a skill store wired in.
	skillStore := skills.NewStore(t.TempDir())
	if err := skillStore.Save(&skills.Skill{Name: "deploy-check", Description: "verify deploys", Status: skills.StatusPending, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	// A skill whose description names a topic earns a skill→topic reference.
	if err := skillStore.Save(&skills.Skill{Name: "work-report", Description: "summarize the work log", Status: skills.StatusActive, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{
		Store:      newTestStore(t),
		Projects:   memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memory.Limits{}),
		Cfg:        cfg,
		SkillStore: skillStore,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := graphNodeIDs(t, out)
	for _, want := range []string{"topic:work", "skill:deploy-check", "daily"} {
		if !ids[want] {
			t.Errorf("missing node %s", want)
		}
	}
	// [from:日志] inside MEMORY.md is the diary feeding long-term memory —
	// two promoted entries make the edge twice as strong.
	if w := graphEdgeWeight(out, "daily", "core:memory", "promotion"); w != 2 {
		t.Errorf("promotion weight = %d, want 2: %v", w, out["edges"])
	}
	// MEMORY.md naming "work" twice is a reference edge with weight 2.
	if w := graphEdgeWeight(out, "core:memory", "topic:work", "reference"); w != 2 {
		t.Errorf("reference weight = %d, want 2: %v", w, out["edges"])
	}
	// A surface naming a core file is an honest reference edge.
	if !graphHasEdge(out, "core:memory", "core:user", "reference") {
		t.Errorf("reference edge core:memory→core:user missing: %v", out["edges"])
	}
	// A skill's description naming a topic links skill→topic.
	if !graphHasEdge(out, "skill:work-report", "topic:work", "reference") {
		t.Errorf("reference edge skill:work-report→topic:work missing: %v", out["edges"])
	}
}

// The response must be byte-identical across requests — edges dedup through a
// map, so any ordering leak would surface as flaky diffs in the UI and tests.
func TestMemoryGraphDeterministic(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memory.Limits{})
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{
		"alpha " + memory.PromotionSourceTag,
		"mentions beta-topic",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("beta-topic", memory.MemFile{Entries: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	var first []byte
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedReq(http.MethodGet, "/api/memory/graph", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if first == nil {
			first = append([]byte(nil), rec.Body.Bytes()...)
			continue
		}
		if !bytes.Equal(first, rec.Body.Bytes()) {
			t.Fatalf("request %d returned different bytes:\n%s\n%s", i, first, rec.Body.Bytes())
		}
	}
}

// mentionsName is the mention scanner: word-boundary-aware for ASCII so
// "go" in "golang" does not link; plain substring for CJK.
func TestMentionsName(t *testing.T) {
	cases := []struct {
		content, name string
		want          bool
	}{
		{"the work calendar", "work", true},
		{"see golang docs", "go", false},
		{"uses go.", "go", true},
		{"go is great", "go", true},
		{"the deploy-check runs", "deploy-check", true},
		{"deploy-checks daily", "deploy-check", false},
		{"提到工作主题", "工作", true},
	}
	for _, c := range cases {
		if got := mentionsName(c.content, c.name); got != c.want {
			t.Errorf("mentionsName(%q, %q) = %v, want %v", c.content, c.name, got, c.want)
		}
	}
	if mentionable("ab") || !mentionable("abc") {
		t.Error("mentionable: names under 3 runes must be skipped")
	}
}
