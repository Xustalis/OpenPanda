// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// newMemoryPanel builds a panel handler with a configured memory root and
// projects store, so the memory endpoints exercise the real config-driven
// caps. Limits are shrunk so cap enforcement is testable with tiny payloads.
func newMemoryPanel(t *testing.T) (http.Handler, *config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.MemoryPath = t.TempDir()
	cfg.Storage.ProjectsPath = t.TempDir()
	cfg.Memory.Limits.User = 40
	cfg.Memory.Limits.Memory = 60
	cfg.Memory.Limits.Project = 80
	limits := memory.Limits{
		User:    cfg.Memory.Limits.User,
		Memory:  cfg.Memory.Limits.Memory,
		Project: cfg.Memory.Limits.Project,
	}
	h := New(Deps{
		Store:     newTestStore(t),
		Projects:  memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, limits),
		Cfg:       cfg,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
	return h, cfg
}

func doJSON(t *testing.T, h http.Handler, req *http.Request) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var out map[string]any
	if rr.Code/100 == 2 {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, out
}

func TestGetMemoryExposesTopicsAndConfiguredLimits(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memory.Limits{User: 40, Memory: 60})
	if err := hermes.SaveUser(memory.MemFile{Entries: []string{"user likes tea"}}); err != nil {
		t.Fatal(err)
	}
	if err := hermes.SaveTopic("work", memory.MemFile{Entries: []string{"team ships fridays"}}); err != nil {
		t.Fatal(err)
	}

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out["user_limit"] != float64(40) || out["mem_limit"] != float64(60) || out["project_limit"] != float64(80) {
		t.Errorf("limits must reflect config: %+v", out)
	}
	topics, ok := out["topics"].([]any)
	if !ok || len(topics) != 1 {
		t.Fatalf("topics = %v, want the one work topic", out["topics"])
	}
	topic := topics[0].(map[string]any)
	if topic["name"] != "work" || !strings.Contains(topic["content"].(string), "team ships fridays") {
		t.Errorf("topic payload wrong: %+v", topic)
	}
}

func TestPutMemoryEnforcesConfiguredLimit(t *testing.T) {
	h, cfg := newMemoryPanel(t)

	// Over the configured 40-char user cap: rejected with the store error.
	body := `{"content":"` + strings.Repeat("x", 50) + `"}`
	req := authedReq(http.MethodPut, "/api/memory/user", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit status = %d, want 400", rr.Code)
	}

	// Within the cap: accepted, and the file lands on disk.
	code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/user", strings.NewReader(`{"content":"user likes tea"}`)))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Storage.MemoryPath, "USER.md"))
	if err != nil || !strings.Contains(string(data), "user likes tea") {
		t.Errorf("USER.md not written: %q err %v", data, err)
	}
}

func TestPutMemoryTopicCRUDAndTraversal(t *testing.T) {
	h, cfg := newMemoryPanel(t)

	code, out := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/topics/work", strings.NewReader(`{"content":"team ships fridays"}`)))
	if code != http.StatusOK {
		t.Fatalf("create topic status = %d", code)
	}
	if out["limit"] != float64(60) {
		t.Errorf("topic limit = %v, want configured 60", out["limit"])
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.MemoryPath, "topics", "work.md")); err != nil {
		t.Fatalf("topic file missing: %v", err)
	}

	// GET surfaces the new topic.
	_, got := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if topics := got["topics"].([]any); len(topics) != 1 {
		t.Fatalf("topics = %v", got["topics"])
	}

	// DELETE removes it; deleting again is a 404.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/memory/topics/work", nil)); code != http.StatusOK {
		t.Fatalf("delete status = %d", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/memory/topics/work", nil)); code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", code)
	}

	// Directory traversal: encoded separators must never write outside
	// topics/, regardless of the status the mux answers with.
	for _, evil := range []string{
		"/api/memory/topics/..%2Fevil",
		"/api/memory/topics/..%2F..%2Fevil",
		"/api/memory/topics/%2e%2e",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPut, evil, strings.NewReader(`{"content":"pwned"}`)))
	}
	for _, dir := range []string{cfg.Storage.MemoryPath, filepath.Dir(cfg.Storage.MemoryPath)} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "evil") || e.Name() == ".." {
				t.Fatalf("traversal escaped topics/: %s/%s", dir, e.Name())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.MemoryPath, "..", "evil.md")); !os.IsNotExist(err) {
		t.Fatalf("evil.md written outside topics/: %v", err)
	}

	// Over the memory cap the topic write is rejected like any other file.
	big := `{"content":"` + strings.Repeat("b", 100) + `"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/memory/topics/big", strings.NewReader(big)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit topic status = %d, want 400", rr.Code)
	}
}

func TestDeleteMemoryDaily(t *testing.T) {
	h, cfg := newMemoryPanel(t)

	dailyDir := filepath.Join(cfg.Storage.MemoryPath, "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dailyDir, "2026-10-01.md"), []byte("- 10:00:00 log"), 0o644); err != nil {
		t.Fatal(err)
	}

	// DELETE removes it; deleting again is a 404.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/memory/daily/2026-10-01", nil)); code != http.StatusOK {
		t.Fatalf("delete status = %d", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/memory/daily/2026-10-01", nil)); code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", code)
	}

	// Traversal names must never resolve outside daily/.
	for _, evil := range []string{
		"/api/memory/daily/..%2Fevil",
		"/api/memory/daily/..%2F..%2Fevil",
		"/api/memory/daily/%2e%2e",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodDelete, evil, nil))
		if rr.Code == http.StatusOK {
			t.Fatalf("traversal delete %s accepted", evil)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.MemoryPath, "..", "evil.md")); !os.IsNotExist(err) {
		t.Fatalf("evil.md deleted outside daily/: %v", err)
	}

	// The archive directory is not a daily file — deleting "archive" 404s.
	if err := os.MkdirAll(filepath.Join(dailyDir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/memory/daily/archive", nil)); code != http.StatusNotFound {
		t.Fatalf("archive delete status = %d, want 404", code)
	}
}

func TestPruneMemoryDaily(t *testing.T) {
	h, cfg := newMemoryPanel(t)

	dailyDir := filepath.Join(cfg.Storage.MemoryPath, "daily")
	if err := os.MkdirAll(dailyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -100).Format("2006-01-02") + ".md"
	recent := time.Now().Format("2006-01-02") + ".md"
	for _, name := range []string{old, recent} {
		if err := os.WriteFile(filepath.Join(dailyDir, name), []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, out := doJSON(t, h, authedReq(http.MethodPost, "/api/memory/daily/prune", nil))
	if code != http.StatusOK || out["status"] != "pruned" {
		t.Fatalf("prune status = %d out = %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(dailyDir, old)); !os.IsNotExist(err) {
		t.Errorf("expired daily should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dailyDir, recent)); err != nil {
		t.Errorf("recent daily should remain: %v", err)
	}
}

func TestPutProjectMemory(t *testing.T) {
	h, cfg := newMemoryPanel(t)

	code, out := doJSON(t, h, authedReq(http.MethodPut, "/api/projects/demo/memory", strings.NewReader(`{"content":"demo uses go"}`)))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out["limit"] != float64(80) {
		t.Errorf("limit = %v, want configured 80", out["limit"])
	}
	data, err := os.ReadFile(filepath.Join(cfg.Storage.ProjectsPath, "demo", "MEMORY.md"))
	if err != nil || !strings.Contains(string(data), "demo uses go") {
		t.Fatalf("project memory not written: %q err %v", data, err)
	}

	// Traversal names are rejected.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/projects/..%2Fevil/memory", strings.NewReader(`{"content":"x"}`)))
	if rr.Code == http.StatusOK {
		t.Fatalf("traversal project name accepted")
	}

	// The configured project cap applies.
	version := memory.Version(memory.ParseMem(data).Bytes())
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/projects/demo/memory", memoryBody(t, strings.Repeat("p", 100), version)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("over-limit project memory status = %d, want 400", rr.Code)
	}
}

// TestGetMemoryDailyFiltersNonDateFiles: a stray *.md dropped into daily/ is
// not a diary page — only real YYYY-MM-DD names may be listed.
func TestGetMemoryDailyFiltersNonDateFiles(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	dailyDir := filepath.Join(cfg.Storage.MemoryPath, "daily")
	if err := os.MkdirAll(dailyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"2026-10-01.md": "- 10:00:00 log",
		"README.md":     "not a diary",
		"notes.md":      "not a diary either",
	} {
		if err := os.WriteFile(filepath.Join(dailyDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	daily, _ := out["daily"].([]any)
	if len(daily) != 1 {
		t.Fatalf("daily list = %v, want exactly the dated file", daily)
	}
	first, _ := daily[0].(map[string]any)
	if first["name"] != "2026-10-01" {
		t.Fatalf("daily[0].name = %v, want 2026-10-01", first["name"])
	}
}

// TestPutDreamsTightensPerms: DREAMS.md holds promoted memory content — the
// console write must keep the 0600 memory-file contract, not 0644.
func TestPutDreamsTightensPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are best-effort on Windows")
	}
	h, cfg := newMemoryPanel(t)
	code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/dreams", strings.NewReader(`{"content":"## 2026-10-18\n- note"}`)))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	fi, err := os.Stat(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("DREAMS.md mode = %o, want 0600", fi.Mode().Perm())
	}
}

// TestReadFileCappedRuneBoundary: the 64 KiB view cap must cut on a rune
// boundary — an all-CJK file hits a mid-rune byte offset with certainty.
func TestReadFileCappedRuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("中", maxMemoryView)), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readFileCapped(path)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8")
	}
	if !strings.Contains(got, "已截断") {
		t.Fatalf("truncation marker missing")
	}
}

func memoryBody(t *testing.T, content, version string) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(map[string]string{"content": content, "version": version})
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b))
}

func TestMemoryEditsRejectStaleVersions(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	_, snapshot := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	versions := snapshot["versions"].(map[string]any)
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	if err := hermes.UpdateUser(func(m *memory.MemFile) error { return m.Add("another writer") }); err != nil {
		t.Fatal(err)
	}
	code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/user", memoryBody(t, "old draft", versions["user"].(string))))
	if code != http.StatusConflict {
		t.Errorf("stale user edit: %d, want 409", code)
	}
	if m, err := hermes.LoadUser(); err != nil || len(m.Entries) != 1 || m.Entries[0] != "another writer" {
		t.Errorf("user data lost: %+v, %v", m, err)
	}

	if code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/topics/work", memoryBody(t, "initial", ""))); code != http.StatusOK {
		t.Fatalf("create topic: %d", code)
	}
	_, snapshot = doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	topicVersion := snapshot["topics"].([]any)[0].(map[string]any)["version"].(string)
	if err := hermes.UpdateTopic("work", func(m *memory.MemFile) error { return m.Add("new topic fact") }); err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, h, authedReq(http.MethodPut, "/api/memory/topics/work", memoryBody(t, "stale", topicVersion)))
	if code != http.StatusConflict {
		t.Errorf("stale topic edit: %d, want 409", code)
	}

	_, project := doJSON(t, h, authedReq(http.MethodGet, "/api/projects/work/memory", nil))
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	if err := projects.Update("work", func(m *memory.MemFile) error { return m.Add("new project fact") }); err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, h, authedReq(http.MethodPut, "/api/projects/work/memory", memoryBody(t, "stale", project["version"].(string))))
	if code != http.StatusConflict {
		t.Errorf("stale project edit: %d, want 409", code)
	}
}

func TestDreamsEditIncludesBeyond64KiBAndRejectsConcurrentAppend(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	large := strings.Repeat("log line\n", 10000)
	if code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/dreams", memoryBody(t, large, ""))); code != http.StatusOK {
		t.Fatalf("save large diary: %d", code)
	}
	_, snapshot := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if snapshot["dreams"] != large || snapshot["dreams_truncated"] != false {
		t.Fatal("diary was truncated before the edit cap")
	}
	version := snapshot["versions"].(map[string]any)["dreams"].(string)
	diary := memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
	if err := diary.Append(memory.Report{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/dreams", memoryBody(t, large+"edit", version)))
	if code != http.StatusConflict {
		t.Errorf("stale diary edit: %d, want 409", code)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
	if err != nil || !strings.Contains(string(data), "Promoted to long-term") {
		t.Errorf("append was lost: %v", err)
	}
}

func TestEmptyTopicCreationAndUnversionedOverwriteProtection(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	// The create UI submits an empty topic; it must become a visible file.
	put := func(path, content, version string) int {
		t.Helper()
		code, _ := doJSON(t, h, authedReq(http.MethodPut, path, memoryBody(t, content, version)))
		return code
	}
	if code := put("/api/memory/topics/empty", "", ""); code != http.StatusOK {
		t.Fatalf("create empty topic: %d", code)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.MemoryPath, "topics", "empty.md")); err != nil {
		t.Fatalf("empty topic not created: %v", err)
	}
	_, snapshot := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if topics := snapshot["topics"].([]any); len(topics) != 1 {
		t.Fatalf("empty topic not listed: %v", topics)
	}
	if code := put("/api/memory/topics/empty", "first entry", ""); code != http.StatusOK {
		t.Fatalf("first write to empty topic: %d", code)
	}
	if code := put("/api/memory/topics/empty", "stale rewrite", ""); code != http.StatusConflict {
		t.Errorf("unversioned topic overwrite: %d, want 409", code)
	}
	if code := put("/api/memory/user", "user likes tea", ""); code != http.StatusOK {
		t.Fatalf("first user write: %d", code)
	}
	if code := put("/api/memory/user", "forget tea", ""); code != http.StatusConflict {
		t.Errorf("unversioned user overwrite: %d, want 409", code)
	}
	if code := put("/api/projects/work/memory", "project fact", ""); code != http.StatusOK {
		t.Fatalf("first project write: %d", code)
	}
	if code := put("/api/projects/work/memory", "stale project", ""); code != http.StatusConflict {
		t.Errorf("unversioned project overwrite: %d, want 409", code)
	}
	if code := put("/api/memory/dreams", "## 2026-01-01\n", ""); code != http.StatusOK {
		t.Fatalf("first diary write: %d", code)
	}
	if code := put("/api/memory/dreams", "erase diary", ""); code != http.StatusConflict {
		t.Errorf("unversioned diary overwrite: %d, want 409", code)
	}
}

func TestDreamsPreviewHashesFullFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DREAMS.md")
	data := []byte(strings.Repeat("2026-01-01\n", 200000) + "recent section")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	preview := readDreams(path)
	if !preview.Truncated || len(preview.Content) > maxDreamsEdit || !utf8.ValidString(preview.Content) {
		t.Fatalf("preview not bounded/valid: %d bytes, truncated=%t", len(preview.Content), preview.Truncated)
	}
	if preview.Version != memory.Version(data) {
		t.Fatal("preview version omitted the unshown tail")
	}
}

func TestEditableMemoryDoesNotTruncateAtPreviewLimit(t *testing.T) {
	_, cfg := newMemoryPanel(t)
	cfg.Memory.Limits.User = 100000
	original := strings.Repeat("记", maxMemoryView/len("记")+1) // beyond 64 KiB, under configured cap
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})
	if code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/user", memoryBody(t, original, ""))); code != http.StatusOK {
		t.Fatalf("first large write: %d", code)
	}
	_, snapshot := doJSON(t, h, authedReq(http.MethodGet, "/api/memory", nil))
	if snapshot["user"] != original {
		t.Fatal("editable USER.md truncated before configured cap")
	}
	version := snapshot["versions"].(map[string]any)["user"].(string)
	if code, _ := doJSON(t, h, authedReq(http.MethodPut, "/api/memory/user", memoryBody(t, original+"后", version))); code != http.StatusOK {
		t.Fatalf("safe edit of >64 KiB memory: %d", code)
	}
}

func TestListDailyIgnoresSymlinkedFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("external secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "2026-08-13.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if files, err := listDaily(dir); err != nil || len(files) != 0 {
		t.Fatalf("daily preview followed a symlink: %+v", files)
	}
}

func TestDailyReadErrorsAreNotEmptyMemory(t *testing.T) {
	h, cfg := newMemoryPanel(t)
	before := httptest.NewRecorder()
	h.ServeHTTP(before, authedReq(http.MethodGet, "/api/memory/graph", nil))
	if before.Code != http.StatusOK {
		t.Fatalf("baseline graph status: %d", before.Code)
	}
	if err := os.WriteFile(filepath.Join(cfg.Storage.MemoryPath, "daily"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/memory"} {
		rr := httptest.NewRecorder()
		req := authedReq(http.MethodGet, path, nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s status = %d, want read error, not empty memory", path, rr.Code)
		}
	}
	// The graph depends only on long-term facts. A corrupt daily surface
	// still fails the diary API, but does not invalidate the cached graph.
	rr := httptest.NewRecorder()
	req := authedReq(http.MethodGet, "/api/memory/graph", nil)
	req.Header.Set("If-None-Match", before.Header().Get("ETag"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("daily history affected long-term graph: %d", rr.Code)
	}
}
