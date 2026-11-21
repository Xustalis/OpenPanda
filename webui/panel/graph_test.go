// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
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

// A fresh node answers 200 with an empty graph, not invented file nodes.
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
	if ids := graphNodeIDs(t, out); len(ids) != 0 {
		t.Errorf("empty memory produced nodes: %+v", ids)
	}
}

func graphNode(t *testing.T, out map[string]any, id string) map[string]any {
	t.Helper()
	raw, _ := out["nodes"].([]any)
	for _, n := range raw {
		if m, ok := n.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	t.Fatalf("node %s missing: %v", id, out["nodes"])
	return nil
}

func TestMemoryGraphOnlyRetainedDurableFacts(t *testing.T) {
	_, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{"姓名：张三", "用户偏好中文回答", "今天想喝奶茶", "hello"}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"Server is Ubuntu 24.04", "task completed successfully"}}); err != nil {
		t.Fatal(err)
	}
	if err := memory.NewDaily(hermes.WarmDir()).Append(time.Now(), "QuantumNoise QuantumNoise QuantumNoise"); err != nil {
		t.Fatal(err)
	}
	store := skills.NewStore(t.TempDir())
	if err := store.Save(&skills.Skill{Name: "noise-skill", Description: "QuantumNoise", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, SkillStore: store, StaticDir: t.TempDir(), Token: testToken})
	status, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph?co=1", nil))
	if status != http.StatusOK {
		t.Fatalf("graph: %d %v", status, out)
	}
	ids := graphNodeIDs(t, out)
	for _, id := range []string{"core:user", "core:memory", "entity:core:user:identity:user.name", "entity:core:user:preference:preference.language", "entity:core:memory:environment:environment.os"} {
		if !ids[id] {
			t.Errorf("missing %s: %v", id, out)
		}
	}
	if len(ids) != 5 {
		t.Fatalf("noise entered graph: %v", out)
	}
	n := graphNode(t, out, "entity:core:user:identity:user.name")
	if n["label"] != "姓名：张三" || n["category"] != "identity" {
		t.Fatalf("identity: %v", n)
	}
	if !graphHasEdge(out, "core:user", "entity:core:user:identity:user.name", "mention") {
		t.Fatal("missing fact provenance")
	}
	// Projection preserves legacy source files for manual review.
	raw, _ := os.ReadFile(memory.UserPath(cfg.Storage.MemoryPath))
	if !strings.Contains(string(raw), "今天想喝奶茶") {
		t.Fatal("graph mutated source data")
	}
}

func TestMemoryGraphUpdatesSameNodeAndReconcilesLegacyCopies(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"用户偏好中文回答"}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("preferences", memory.MemFile{Entries: []string{"用户偏好中文回答"}}); err != nil {
		t.Fatal(err)
	}
	tool := memory.NewTool(hermes, nil)
	add := func(text string) {
		t.Helper()
		if _, err := tool.Execute(memory.ToolAdd, map[string]any{"target": "user", "entry": text}); err != nil {
			t.Fatal(err)
		}
	}
	add("用户偏好英文回答")
	_, first := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	id := "entity:core:user:preference:preference.language"
	if graphNode(t, first, id)["label"] != "用户偏好英文回答" {
		t.Fatal(first)
	}
	add("用户偏好中文回答")
	_, second := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if graphNode(t, second, id)["label"] != "用户偏好中文回答" || len(graphNodeIDs(t, second)) != 2 {
		t.Fatal(second)
	}
	mem, _ := hermes.LoadMemory()
	topic, _ := hermes.LoadTopic("preferences")
	if len(mem.Entries) != 0 || len(topic.Entries) != 0 {
		t.Fatalf("stale copies: %v %v", mem, topic)
	}
	if _, err := tool.Execute(memory.ToolRemove, map[string]any{"target": "user", "old": "用户偏好中文"}); err != nil {
		t.Fatal(err)
	}
	_, empty := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if len(graphNodeIDs(t, empty)) != 0 {
		t.Fatalf("forgotten fact remains: %v", empty)
	}
}

func TestMemoryGraphSeparatesProjectAttributes(t *testing.T) {
	_, cfg := newMemoryPanel(t)
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	for name, command := range map[string]string{"alpha": "go test ./...", "beta": "npm test"} {
		fact := memory.Fact{Category: "project", Key: "test.command", Text: command}
		if err := projects.Save(name, memory.MemFile{Entries: []string{fact.Entry(), "今天修复测试", "用户偏好中文"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, Projects: projects, StaticDir: t.TempDir(), Token: testToken})
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if len(graphNodeIDs(t, out)) != 4 {
		t.Fatal(out)
	}
	if graphNode(t, out, "entity:project:alpha:project:test.command")["label"] != "go test ./..." {
		t.Fatal(out)
	}
	if graphNode(t, out, "entity:project:beta:project:test.command")["label"] != "npm test" {
		t.Fatal(out)
	}
}

func TestMemoryGraphReceiptRequiresRetainedFact(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	fact := memory.Fact{Category: "preference", Key: "preference.language", Text: "用户偏好中文回答"}
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{fact.Entry()}}); err != nil {
		t.Fatal(err)
	}
	diary := memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
	if err := diary.Append(memory.Report{Promoted: []string{fact.Entry(), "task completed"}}, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if graphEdgeWeight(out, "core:dreams", "core:user", "promotion") != 1 {
		t.Fatal(out)
	}
	node := graphNode(t, out, "core:dreams")
	if node["first_seen"] != "2026-01-03" {
		t.Fatal(node)
	}
	if err := hermes.SaveUser(memory.MemFile{}); err != nil {
		t.Fatal(err)
	}
	_, out = doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if len(graphNodeIDs(t, out)) != 0 {
		t.Fatalf("obsolete receipt: %v", out)
	}
}

func TestMemoryGraphReceiptBeyondPreview(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"Server is Ubuntu 24.04"}}); err != nil {
		t.Fatal(err)
	}
	content := "## 2026-01-01\n" + strings.Repeat("- routine note\n", 6000) + "## 2026-02-02\n- Promoted to long-term memory / 提升到长期记忆：\n  - Server is Ubuntu 24.04\n"
	if err := os.WriteFile(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if graphEdgeWeight(out, "core:dreams", "core:memory", "promotion") != 1 {
		t.Fatal(out)
	}
}

func TestMemoryGraphReceiptUsesSelectedCanonicalSurface(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	current, old := "用户偏好英文回答", "用户偏好中文回答"
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{current}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{old}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("prefs", memory.MemFile{Entries: []string{current}}); err != nil {
		t.Fatal(err)
	}
	diary := memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
	if err := diary.Append(memory.Report{Promoted: []string{old, current}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	ids := graphNodeIDs(t, out)
	if status != http.StatusOK || len(ids) != 3 || graphEdgeWeight(out, "core:dreams", "core:user", "promotion") != 1 {
		t.Fatalf("canonical receipt missing: %d %v", status, out)
	}
	for _, raw := range out["edges"].([]any) {
		edge := raw.(map[string]any)
		if !ids[edge["from"].(string)] || !ids[edge["to"].(string)] {
			t.Fatalf("receipt links to a suppressed copy: %v", edge)
		}
	}
}

func TestMemoryGraphCachingAndDeterminism(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{"姓名：张三"}}); err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	h.ServeHTTP(first, authedReq(http.MethodGet, "/api/memory/graph", nil))
	etag := first.Header().Get("ETag")
	if etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal(first.Header())
	}
	for i := 0; i < 3; i++ {
		next := httptest.NewRecorder()
		h.ServeHTTP(next, authedReq(http.MethodGet, "/api/memory/graph", nil))
		if !bytes.Equal(first.Body.Bytes(), next.Body.Bytes()) {
			t.Fatal("nondeterministic graph")
		}
	}
	if err := memory.NewDaily(hermes.WarmDir()).Append(time.Now(), "task finished"); err != nil {
		t.Fatal(err)
	}
	req := authedReq(http.MethodGet, "/api/memory/graph", nil)
	req.Header.Set("If-None-Match", etag)
	cached := httptest.NewRecorder()
	h.ServeHTTP(cached, req)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("daily log invalidated graph: %d", cached.Code)
	}
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{"姓名：李四"}}); err != nil {
		t.Fatal(err)
	}
	changed := httptest.NewRecorder()
	h.ServeHTTP(changed, req)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatal("fact change cached")
	}
}

func TestMemoryGraphETagIgnoresDirectoryTimestamps(t *testing.T) {
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	if err := hermes.SaveMemory(memory.MemFile{Entries: []string{"Server is Ubuntu 24.04"}}); err != nil {
		t.Fatal(err)
	}
	first := memoryGraphETag(20, false, root)
	past := time.Unix(1, 0)
	if err := os.Chtimes(root, past, past); err != nil {
		t.Fatal(err)
	}
	if next := memoryGraphETag(20, false, root); next != first {
		t.Fatal("directory timestamps changed ETag")
	}
	if err := hermes.SaveTopic("ops", memory.MemFile{Entries: []string{"Server is Debian"}}); err != nil {
		t.Fatal(err)
	}
	if next := memoryGraphETag(20, false, root); next == first {
		t.Fatal("new source did not change ETag")
	}
}

func TestDreamPromotionCountRejectsUnverifiedTags(t *testing.T) {
	if n := dreamPromotionCount("", "manual "+memory.PromotionSourceTag); n != 0 {
		t.Fatal(n)
	}
	if n := dreamPromotionCount("## 2026-01-03\n- Promoted to long-term memory / 提升到长期记忆：\n  - trusted fact\n", "trusted fact"); n != 1 {
		t.Fatal(n)
	}
}

func TestMemoryGraphDoesNotTruncateOrHideUnreadableFacts(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	text := "[fact:convention:large] " + strings.Repeat("retain ", 10000)
	if err := os.WriteFile(memory.MemoryPath(cfg.Storage.MemoryPath), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	node := graphNode(t, out, "entity:core:memory:convention:large")
	if node["label"] != strings.TrimSpace(strings.TrimPrefix(text, "[fact:convention:large] ")) {
		t.Fatal("retained assertion was truncated")
	}
	if err := os.Remove(memory.MemoryPath(cfg.Storage.MemoryPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(memory.MemoryPath(cfg.Storage.MemoryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt memory treated as empty: %d", rr.Code)
	}
}

// graphFixture seeds USER.md, MEMORY.md, one topic and one project with n
// facts each so the entity cap, not the data, decides what is shown.
func graphFixture(t *testing.T, perSurface int) (http.Handler, *config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.MemoryPath = t.TempDir()
	cfg.Storage.ProjectsPath = t.TempDir()
	cfg.Memory.Limits.User = 20000
	cfg.Memory.Limits.Memory = 20000
	cfg.Memory.Limits.Project = 20000
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memory.Limits{User: 20000, Memory: 20000, Project: 20000})
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memory.Limits{Project: 20000})
	mk := func(category, prefix string) memory.MemFile {
		var m memory.MemFile
		for i := 0; i < perSurface; i++ {
			f := memory.Fact{Category: category, Key: fmt.Sprintf("%s.item%02d", prefix, i), Text: fmt.Sprintf("%s fact number %02d", prefix, i)}
			m.Entries = append(m.Entries, f.Entry())
		}
		return m
	}
	if err := hermes.SaveUser(mk("preference", "preference")); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveMemory(mk("environment", "environment")); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("ops", mk("convention", "convention")); err != nil {
		t.Fatal(err)
	}
	if err := projects.Save("demo", mk("project", "project")); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, Projects: projects, StaticDir: t.TempDir(), Token: testToken})
	return h, cfg
}

func graphMeta(t *testing.T, out map[string]any) (total, shown, limit int) {
	t.Helper()
	meta, ok := out["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing: %v", out)
	}
	num := func(k string) int {
		v, ok := meta[k].(float64)
		if !ok {
			t.Fatalf("meta.%s missing: %v", k, meta)
		}
		return int(v)
	}
	return num("entities_total"), num("entities_shown"), num("entity_limit")
}

func entitiesPerSurface(t *testing.T, out map[string]any) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for id := range graphNodeIDs(t, out) {
		if !strings.HasPrefix(id, "entity:") {
			continue
		}
		parts := strings.SplitN(id, ":", 4)
		switch parts[1] {
		case "core":
			counts["core:"+parts[2]]++
		default:
			counts[parts[1]+":"+parts[2]]++
		}
	}
	return counts
}

// One chatty surface must not starve the others of fact nodes: with a cap of
// 8 over four surfaces holding 30 facts each, every surface shows two.
func TestMemoryGraphSharesEntityCapAcrossSurfaces(t *testing.T) {
	h, _ := graphFixture(t, 30)
	status, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph?entities=8", nil))
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	counts := entitiesPerSurface(t, out)
	want := map[string]int{"core:user": 2, "core:memory": 2, "topic:ops": 2, "project:demo": 2}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("surface %s shows %d facts, want %d (all: %v)", k, counts[k], n, counts)
		}
	}
	total, shown, limit := graphMeta(t, out)
	if total != 120 || shown != 8 || limit != 8 {
		t.Errorf("meta = total %d shown %d limit %d, want 120/8/8", total, shown, limit)
	}
}

// An uneven cap hands the remainder out in surface order, and a surface with
// fewer facts than its share gives the slack back to the others.
func TestMemoryGraphEntityCapRemainderAndSlack(t *testing.T) {
	h, cfg := graphFixture(t, 30)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memory.Limits{User: 20000, Memory: 20000})
	one := memory.Fact{Category: "preference", Key: "preference.only", Text: "only one"}
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{one.Entry()}}); err != nil {
		t.Fatal(err)
	}
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph?entities=10", nil))
	counts := entitiesPerSurface(t, out)
	if counts["core:user"] != 1 {
		t.Errorf("user surface = %d, want its single fact", counts["core:user"])
	}
	sum := 0
	for _, n := range counts {
		sum += n
	}
	if sum != 10 {
		t.Errorf("shown %d facts, want the full cap of 10 (slack must be redistributed): %v", sum, counts)
	}
	// 9 left over three surfaces: 3 each.
	for _, k := range []string{"core:memory", "topic:ops", "project:demo"} {
		if counts[k] != 3 {
			t.Errorf("%s = %d, want 3: %v", k, counts[k], counts)
		}
	}
}

// Within one surface the original order is kept: the first facts win.
func TestMemoryGraphEntityOrderWithinSurface(t *testing.T) {
	h, _ := graphFixture(t, 30)
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph?entities=8", nil))
	ids := graphNodeIDs(t, out)
	for _, id := range []string{"entity:core:memory:environment:environment.item00", "entity:core:memory:environment:environment.item01"} {
		if !ids[id] {
			t.Errorf("missing %s", id)
		}
	}
	if ids["entity:core:memory:environment:environment.item02"] {
		t.Error("later fact outranked an earlier one on the same surface")
	}
}

func TestMemoryGraphEntityLimitBounds(t *testing.T) {
	h, _ := graphFixture(t, 40)
	cases := []struct {
		query string
		limit int
	}{
		{"", 20},
		{"?entities=0", 20},
		{"?entities=-3", 20},
		{"?entities=abc", 20},
		{"?entities=60", 60},
		{"?entities=120", 120},
		{"?entities=9999", 120},
	}
	for _, c := range cases {
		_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph"+c.query, nil))
		total, shown, limit := graphMeta(t, out)
		if limit != c.limit {
			t.Errorf("%q: limit %d, want %d", c.query, limit, c.limit)
		}
		if want := min(c.limit, total); shown != want {
			t.Errorf("%q: shown %d, want %d", c.query, shown, want)
		}
		if total != 160 {
			t.Errorf("%q: total %d, want 160", c.query, total)
		}
	}
}

// When everything fits, nothing is reported as hidden.
func TestMemoryGraphMetaWhenNothingTruncated(t *testing.T) {
	h, _ := graphFixture(t, 3)
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph?entities=60", nil))
	total, shown, limit := graphMeta(t, out)
	if total != 12 || shown != 12 || limit != 60 {
		t.Errorf("meta = %d/%d/%d, want 12/12/60", total, shown, limit)
	}
}

func TestMemoryGraphEmptyHasZeroMeta(t *testing.T) {
	h, _ := newMemoryPanel(t)
	_, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory/graph", nil))
	total, shown, limit := graphMeta(t, out)
	if total != 0 || shown != 0 || limit != 20 {
		t.Errorf("meta = %d/%d/%d, want 0/0/20", total, shown, limit)
	}
}

// The same inputs always yield byte-identical bodies, and the entity limit is
// part of the cache fingerprint: a 304 must never certify a different cap.
func TestMemoryGraphEntityLimitDeterminismAndETag(t *testing.T) {
	h, _ := graphFixture(t, 30)
	get := func(q, inm string) *httptest.ResponseRecorder {
		req := authedReq(http.MethodGet, "/api/memory/graph"+q, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	a, b := get("?entities=12", ""), get("?entities=12", "")
	if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatal("nondeterministic graph body")
	}
	if c := get("?entities=12", a.Header().Get("ETag")); c.Code != http.StatusNotModified {
		t.Fatalf("same cap not revalidated: %d", c.Code)
	}
	if c := get("?entities=24", a.Header().Get("ETag")); c.Code != http.StatusOK {
		t.Fatalf("different cap answered 304")
	}
}
