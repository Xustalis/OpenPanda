// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentreg "github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"gopkg.in/yaml.v3"
)

// ---- shared helpers ----

// newMigratedDB opens an in-memory migrated database for stores that need a
// raw *sql.DB (reminders, audit) rather than the wrapped TaskStore.
func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

// newEnginePanel builds a panel wired to a real ask engine (bogus model
// endpoint, temp storage) plus a seeded config file, so the settings
// write-paths (PUT /api/settings/*) run against real persistence.
func newEnginePanel(t *testing.T) (http.Handler, *config.Config, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := isolatedConfig(t)
	// A endpoint nothing listens on: construction must succeed without dialing.
	cfg.Model.BaseURL = "http://127.0.0.1:9"
	cfg.Model.Model = "test-model"
	cfg.Model.APIKey = ""
	eng, err := askengine.New(context.Background(), cfg, askengine.Options{})
	if err != nil {
		t.Fatalf("askengine.New: %v", err)
	}
	t.Cleanup(eng.Close)

	configPath := filepath.Join(tmp, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	h := New(Deps{
		Store:      newTestStore(t),
		Engine:     eng,
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, cfg, configPath
}

// jsonReq builds an authorized JSON request.
func jsonReq(method, target, body string) *http.Request {
	req := authedReq(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---- sessions ----

func TestSessionsCRUD(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	// Create: id assigned, title kept, no worktree without a repo.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions", `{"title":"kickoff"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, out)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("create returned empty id: %v", out)
	}

	// List: the new session is there.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions", nil))
	var list []*sessions.Session
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	if len(list) != 1 || list[0].ID != id || list[0].Title != "kickoff" {
		t.Fatalf("list = %+v", list)
	}

	// Get by id; unknown id is a 404.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id, nil))
	if code != http.StatusOK || out["title"] != "kickoff" {
		t.Fatalf("get status = %d, out = %v", code, out)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/nope", nil)); code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", code)
	}

	// Delete: 200 then 404 on re-read.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/sessions/"+id, nil)); code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id, nil)); code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", code)
	}
}

// TestSessionTeamRoster covers the thread's agent roster: it rides create,
// PATCH replaces and clears it, and a name that cannot be a card key is
// refused instead of being silently dropped (a dropped entry is a smaller
// team; a dropped last entry is "no restriction at all").
func TestSessionTeamRoster(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions",
		`{"title":"team","team":["claude_code","opencode"]}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, out)
	}
	id := out["id"].(string)
	team, _ := out["team"].([]any)
	if len(team) != 2 || team[0] != "claude_code" {
		t.Fatalf("created team = %v", out["team"])
	}

	// PATCH replaces the roster, deduping and trimming as it goes.
	code, out = doJSON(t, h, jsonReq(http.MethodPatch, "/api/sessions/"+id,
		`{"team":["codex"," codex ","pi"]}`))
	if code != http.StatusOK {
		t.Fatalf("patch status = %d, out = %v", code, out)
	}
	team, _ = out["team"].([]any)
	if len(team) != 2 || team[0] != "codex" || team[1] != "pi" {
		t.Fatalf("patched team = %v", out["team"])
	}

	// An explicit empty list clears it — no restriction again.
	code, out = doJSON(t, h, jsonReq(http.MethodPatch, "/api/sessions/"+id, `{"team":[]}`))
	if code != http.StatusOK {
		t.Fatalf("clear status = %d, out = %v", code, out)
	}
	if team, ok := out["team"].([]any); ok && len(team) != 0 {
		t.Fatalf("cleared team = %v, want empty", out["team"])
	}

	// A slash can never be a card agent key; refuse it rather than dropping.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPatch, "/api/sessions/"+id, `{"team":["a/b"]}`)); code != http.StatusBadRequest {
		t.Fatalf("invalid team status = %d, want 400", code)
	}
	// An omitted field leaves the roster alone.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPatch, "/api/sessions/"+id, `{"title":"renamed"}`)); code != http.StatusOK {
		t.Fatalf("title-only patch status = %d", code)
	}
}

func TestSessionPinAndBulkDelete(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions", `{"title":"`+title+`"}`))
		if code != http.StatusOK {
			t.Fatalf("create %s status = %d, body %v", title, code, out)
		}
		ids = append(ids, out["id"].(string))
	}

	// Pin the oldest-created session: it must lead the listing despite
	// being the least recently active.
	code, out := doJSON(t, h, jsonReq(http.MethodPatch, "/api/sessions/"+ids[0], `{"pinned":true}`))
	if code != http.StatusOK || out["pinned"] != true {
		t.Fatalf("pin status = %d, out = %v", code, out)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions", nil))
	var list []*sessions.Session
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("list unmarshal: %v", err)
	}
	if len(list) != 3 || list[0].ID != ids[0] || !list[0].Pinned {
		t.Fatalf("pinned order = %+v", list)
	}

	// Bulk delete: two real ids plus a ghost — the ghost lands in failed,
	// the real ones are gone.
	body := `{"ids":["` + ids[0] + `","` + ids[2] + `","ghost-id"]}`
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/bulk-delete", body))
	if code != http.StatusOK {
		t.Fatalf("bulk delete status = %d, body %v", code, out)
	}
	deleted, _ := out["deleted"].([]any)
	failed, _ := out["failed"].([]any)
	if len(deleted) != 2 || len(failed) != 1 {
		t.Fatalf("bulk delete = deleted %v failed %v", deleted, failed)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id, nil)); code != http.StatusNotFound {
			t.Fatalf("get %s after bulk delete status = %d, want 404", id, code)
		}
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+ids[1], nil)); code != http.StatusOK {
		t.Fatalf("untouched session status = %d, want 200", code)
	}

	// Validation: empty ids and oversize batches are rejected.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/bulk-delete", `{"ids":[]}`)); code != http.StatusBadRequest {
		t.Fatalf("empty bulk delete status = %d, want 400", code)
	}
}

func TestSessionForkEndpoint(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessStore,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions", `{"title":"root"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, out)
	}
	parent := out["id"].(string)

	// Seed two turns so the fork has something to copy.
	for _, role := range []string{"user", "assistant"} {
		if _, err := sessStore.AppendTurn(parent, sessions.Turn{Role: role, Text: role + " turn"}); err != nil {
			t.Fatalf("AppendTurn: %v", err)
		}
	}

	code, child := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+parent+"/fork", `{"at":1}`))
	if code != http.StatusOK {
		t.Fatalf("fork status = %d, body %v", code, child)
	}
	if child["parent_id"] != parent {
		t.Fatalf("child parent_id = %v, want %s", child["parent_id"], parent)
	}
	if child["fork_index"].(float64) != 1 {
		t.Fatalf("child fork_index = %v, want 1", child["fork_index"])
	}
	turns, _ := child["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("child turns = %d, want 1 (truncated prefix)", len(turns))
	}

	// Fork of a missing session is a 404; a whole-thread fork omits at.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/nope/fork", `{}`)); code != http.StatusNotFound {
		t.Fatalf("fork missing status = %d, want 404", code)
	}
	code, full := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+parent+"/fork", `{}`))
	if code != http.StatusOK {
		t.Fatalf("full fork status = %d", code)
	}
	if turns, _ := full["turns"].([]any); len(turns) != 2 {
		t.Fatalf("full fork turns = %d, want 2", len(turns))
	}
}

func TestSessionAskValidation(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"invalid json", "{not json", http.StatusBadRequest},
		{"empty prompt", `{"prompt":"   "}`, http.StatusBadRequest},
		{"valid prompt without engine", `{"prompt":"hi"}`, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, jsonReq(http.MethodPost, "/api/sessions/some-id/ask", tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// newPanelGitRepo creates a temporary git repository with one empty commit,
// isolated from the machine's global git config.
func newPanelGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "-q", "-b", "main", dir},
		{"git", "-C", dir, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestSessionWorktreeDiffAndMerge drives the highest-stakes session flow end
// to end over HTTP: a session in a git repo carves a worktree, edits show up
// in the diff, and merging lands the work in the main checkout.
func TestSessionWorktreeDiffAndMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newPanelGitRepo(t)
	wt, err := sessions.OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		Worktrees: wt,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	// Create: the response carries the carved worktree and its branch.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions", `{"title":"repo work"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, out)
	}
	id, _ := out["id"].(string)
	worktree, _ := out["worktree"].(string)
	if id == "" || worktree == "" || out["branch"] != sessions.Branch(id) {
		t.Fatalf("session not worktree-backed: %v", out)
	}

	// Unknown ids on diff/merge are client errors, not 500s.
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/nope/diff", nil)); code != http.StatusBadRequest {
		t.Fatalf("diff unknown status = %d, want 400", code)
	}
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/nope/merge", `{}`)); code != http.StatusBadRequest {
		t.Fatalf("merge unknown status = %d, want 400", code)
	}

	// Session work: a new file in the worktree shows up in Status and Diff.
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id+"/diff", nil))
	if code != http.StatusOK {
		t.Fatalf("diff status = %d", code)
	}
	changes, _ := out["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want the one new file", out["changes"])
	}
	if patch, _ := out["patch"].(string); !strings.Contains(patch, "hello") {
		t.Fatalf("patch missing file content: %q", patch)
	}

	// Merge: the work lands in the main checkout, the worktree goes clean.
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+id+"/merge", `{"message":"session work"}`))
	if code != http.StatusOK || out["merged"] != true {
		t.Fatalf("merge status = %d, out = %v", code, out)
	}
	data, err := os.ReadFile(filepath.Join(repo, "new.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("merged file missing in main checkout: %q err %v", data, err)
	}
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id+"/diff", nil))
	if code != http.StatusOK {
		t.Fatalf("post-merge diff status = %d", code)
	}
	if changes, _ = out["changes"].([]any); len(changes) != 0 {
		t.Fatalf("worktree dirty after merge: %v", out["changes"])
	}
}

// TestSessionWorktreeRevert exercises POST /api/sessions/{id}/revert end to
// end: a merged session rolls the checkout back, and a second revert answers
// 400 rather than pretending it undid something.
func TestSessionWorktreeRevert(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newPanelGitRepo(t)
	wt, err := sessions.OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{
		Store:     newTestStore(t),
		Sessions:  sessions.NewStore(t.TempDir()),
		Worktrees: wt,
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions", `{"title":"repo work"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, out)
	}
	id, _ := out["id"].(string)
	worktree, _ := out["worktree"].(string)
	if err := os.WriteFile(filepath.Join(worktree, "rolled.txt"), []byte("ship me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The diff response carries the merge-preview stat next to the patch.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/sessions/"+id+"/diff", nil))
	if code != http.StatusOK {
		t.Fatalf("diff status = %d", code)
	}
	stat, _ := out["stat"].(map[string]any)
	if stat == nil || stat["untracked"].(float64) != 1 {
		t.Fatalf("stat missing the untracked file: %v", out["stat"])
	}

	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+id+"/merge", `{}`))
	if code != http.StatusOK || out["merged"] != true {
		t.Fatalf("merge status = %d, out = %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "rolled.txt")); err != nil {
		t.Fatalf("merged file missing: %v", err)
	}

	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+id+"/revert", `{}`))
	if code != http.StatusOK || out["reverted"] != true {
		t.Fatalf("revert status = %d, out = %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "rolled.txt")); !os.IsNotExist(err) {
		t.Fatalf("revert left the merged file behind (err=%v)", err)
	}
	// A second revert has nothing recorded left.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/sessions/"+id+"/revert", `{}`)); code != http.StatusBadRequest {
		t.Fatalf("re-revert status = %d, want 400", code)
	}
}

// TestTaskRevertEndpoint exercises POST /api/tasks/{id}/revert: the run's git
// baseline rolls back tracked edits and post-baseline files, and a second
// revert answers 404 once the baseline ref is consumed.
func TestTaskRevertEndpoint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newPanelGitRepo(t)
	store := newTestStore(t)
	h := New(Deps{Store: store, Cfg: config.Default(), StaticDir: t.TempDir(), Token: testToken})
	ctx := context.Background()
	task, err := store.Create(ctx, "", "", "t", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetExecWorkDir(ctx, task.TaskID, repo); err != nil {
		t.Fatalf("exec work dir: %v", err)
	}
	base := filepath.Join(repo, "base.txt")
	if err := os.WriteFile(base, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := defense.SaveGitBaseline(repo, task.TaskID); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := os.WriteFile(base, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(repo, "extra.txt")
	if err := os.WriteFile(extra, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/revert", `{}`))
	if code != http.StatusOK || out["reverted"] != true {
		t.Fatalf("revert status = %d, out = %v", code, out)
	}
	data, _ := os.ReadFile(base)
	if strings.TrimSpace(string(data)) != "v0" {
		t.Fatalf("base.txt = %q, want v0", data)
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Fatalf("revert left extra.txt behind (err=%v)", err)
	}
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/revert", `{}`)); code != http.StatusNotFound {
		t.Fatalf("re-revert status = %d, want 404", code)
	}
}

// ---- model & MCP settings ----

func TestModelSettingsGetMasksKey(t *testing.T) {
	cfg := config.Default()
	cfg.Model.APIKey = "sk-super-secret-1234"
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/settings/model", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "sk-super-secret-1234") {
		t.Fatalf("GET leaked the raw API key: %s", body)
	}
	var got modelSettingsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Head and tail: the tail alone cannot separate two accounts of one vendor.
	if !got.APIKeySet || got.APIKeyHint != "sk-s…1234" {
		t.Fatalf("key hint wrong: %+v", got)
	}
}

func TestModelSettingsPutRoundTrip(t *testing.T) {
	h, _, configPath := newEnginePanel(t)

	// An engine-less panel rejects an empty base_url; with a holder wired a
	// full save hot-loads the engine instead (TestModelSettingsHotLoadEngine).
	// The bare config must clear Default()'s model endpoint: an empty request
	// base_url means "keep the current one", and the default is non-empty.
	bareCfg := config.Default()
	bareCfg.Model.BaseURL = ""
	bare := New(Deps{Store: newTestStore(t), Cfg: bareCfg, StaticDir: t.TempDir(), Token: testToken})
	if code, _ := doJSON(t, bare, jsonReq(http.MethodPut, "/api/settings/model", `{}`)); code != http.StatusBadRequest {
		t.Fatalf("put with empty base_url status = %d, want 400", code)
	}

	body := `{"api_type":"openai","base_url":"http://127.0.0.1:1/v1","model":"gpt-test","max_tokens":128}`
	code, out := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/model", body))
	if code != http.StatusOK {
		t.Fatalf("put status = %d, out = %v", code, out)
	}
	if out["model"] != "gpt-test" || out["api_type"] != "openai" {
		t.Fatalf("put echo wrong: %v", out)
	}

	// GET reflects the hot-swapped engine config.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/settings/model", nil))
	if code != http.StatusOK || out["model"] != "gpt-test" || out["base_url"] != "http://127.0.0.1:1/v1" {
		t.Fatalf("get after put = %v", out)
	}

	// The config file on disk carries the new model.
	data, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(data), "gpt-test") {
		t.Fatalf("config file not updated: %q err %v", data, err)
	}
}

func TestModelSettingsTestEndpoint(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	// A dead endpoint reports ok:false with the transport error, not a 5xx.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/settings/model/test", `{"base_url":"http://127.0.0.1:9","model":"m"}`))
	if code != http.StatusOK {
		t.Fatalf("status = %d, out = %v", code, out)
	}
	if out["ok"] != false || out["error"] == "" {
		t.Fatalf("dead endpoint must report ok:false with error: %v", out)
	}

	// Without an engine the connectivity test still works — the onboarding
	// form verifies a candidate provider before the first save hot-loads
	// the engine. A dead endpoint reports ok:false, not a 5xx.
	bare := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	code, out = doJSON(t, bare, jsonReq(http.MethodPost, "/api/settings/model/test", `{"base_url":"http://127.0.0.1:9","model":"m"}`))
	if code != http.StatusOK || out["ok"] != false {
		t.Fatalf("test without engine = %d %v, want 200 ok:false", code, out)
	}
}

func TestModelSettingsToConfigMerge(t *testing.T) {
	offlineOff := false
	cur := config.ModelConfig{
		Provider:       "minimax",
		Name:           "mm",
		APIType:        "anthropic",
		BaseURL:        "https://api.minimax.cn/anthropic",
		APIKey:         "sk-secret",
		Model:          "MiniMax-M3",
		MaxTokens:      131072,
		ContextWindow:  1000000,
		Thinking:       "on",
		ThinkingBudget: 8192,
		MaxRetries:     3,
		OfflineWait:    &offlineOff,
		NoAuth:         true,
		Auth:           "bearer",
		Params:         map[string]any{"temperature": 0.5},
		Headers:        map[string]string{"X-Tenant": "blue"},
	}

	// A sparse request (form save) must keep every field the form does not
	// carry — secrets, auth override, params, headers, thinking state.
	got := modelSettingsToConfig(modelSettingsJSON{BaseURL: cur.BaseURL, Model: cur.Model, APIType: "openai"}, cur)
	if got.APIKey != "sk-secret" || got.Auth != "bearer" || got.Headers["X-Tenant"] != "blue" || got.Params["temperature"] != 0.5 {
		t.Fatalf("sparse save wiped escape-hatch fields: %+v", got)
	}
	if got.Thinking != "on" || got.ThinkingBudget != 8192 || got.ContextWindow != 1000000 || got.MaxRetries != 3 || !got.NoAuth {
		t.Fatalf("sparse save wiped tuning fields: %+v", got)
	}
	// The gateway has no form control: a save must not re-arm what the YAML
	// turned off.
	if got.OfflineWait == nil || *got.OfflineWait {
		t.Fatalf("sparse save re-armed the offline gate: %+v", got.OfflineWait)
	}
	if got.APIType != "openai" {
		t.Fatalf("api_type not applied: %+v", got)
	}

	// Explicit values win; "auto" resets thinking to the provider default;
	// a null-vs-absent no_auth keeps the stored flag.
	noAuth := false
	got = modelSettingsToConfig(modelSettingsJSON{
		BaseURL:        cur.BaseURL,
		Model:          "MiniMax-M2.7",
		ContextWindow:  intPtr(204800),
		Thinking:       "auto",
		ThinkingBudget: intPtr(4096),
		NoAuth:         &noAuth,
		MaxRetries:     intPtr(9),
	}, cur)
	if got.Thinking != "" || got.ThinkingBudget != 4096 || got.ContextWindow != 204800 || got.NoAuth || got.MaxRetries != 9 {
		t.Fatalf("explicit overrides not applied: %+v", got)
	}
	// An explicit zero resets the knob — the form's empty box means "use the
	// provider/entry default", not "keep whatever was there".
	got = modelSettingsToConfig(modelSettingsJSON{
		BaseURL:        cur.BaseURL,
		Model:          cur.Model,
		MaxTokens:      intPtr(0),
		ContextWindow:  intPtr(0),
		ThinkingBudget: intPtr(0),
		MaxRetries:     intPtr(0),
	}, cur)
	if got.MaxTokens != 0 || got.ContextWindow != 0 || got.ThinkingBudget != 0 || got.MaxRetries != 0 {
		t.Fatalf("explicit zeros did not reset: %+v", got)
	}
	// A negative retry budget is the documented way to disable retries.
	got = modelSettingsToConfig(modelSettingsJSON{MaxRetries: intPtr(-1)}, cur)
	if got.MaxRetries != -1 {
		t.Fatalf("negative retry budget not applied: %+v", got)
	}
	got = modelSettingsToConfig(modelSettingsJSON{BaseURL: cur.BaseURL, Model: cur.Model, Thinking: "off"}, cur)
	if got.Thinking != "off" {
		t.Fatalf("thinking off not applied: %+v", got)
	}
}

func TestModelSettingsPutAdvancedRoundTrip(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	body := `{"api_type":"anthropic","base_url":"https://api.minimax.cn/anthropic","model":"MiniMax-M3","max_tokens":131072,"context_window":1000000,"thinking":"on","thinking_budget":8192,"max_retries":3,"no_auth":false}`
	code, out := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/model", body))
	if code != http.StatusOK {
		t.Fatalf("put status = %d, out = %v", code, out)
	}
	for k, want := range map[string]any{"context_window": 1000000.0, "thinking": "on", "thinking_budget": 8192.0, "no_auth": false} {
		if out[k] != want {
			t.Fatalf("put echo %s = %v, want %v (out=%v)", k, out[k], want, out)
		}
	}
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/settings/model", nil))
	if code != http.StatusOK || out["thinking"] != "on" || out["context_window"] != 1000000.0 || out["thinking_budget"] != 8192.0 {
		t.Fatalf("get after put = %v", out)
	}
}

func TestAddModelAdvancedFields(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	// Custom endpoint on a known vendor URL inherits catalogue tuning and
	// keeps every explicit field the caller supplied.
	body := `{"provider":"custom","base_url":"https://api.minimax.cn/anthropic","model":"MiniMax-M3","api_key":"sk-x","alias":"mmx","thinking":"on","thinking_budget":8192,"max_retries":3,"params":{"temperature":0.5},"headers":{"X-Tenant":"blue"}}`
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models", body))
	if code != http.StatusOK {
		t.Fatalf("add status = %d, out = %v", code, out)
	}
	if out["context_window"] != 1000000.0 || out["max_tokens"] != 131072.0 {
		t.Fatalf("custom detect did not resolve catalogue tuning: %v", out)
	}
	if out["thinking"] != "on" || out["thinking_budget"] != 8192.0 || out["max_retries"] != 3.0 {
		t.Fatalf("explicit tuning not persisted: %v", out)
	}

	// Keyless custom endpoint defaults to no-auth (same rule as the CLI).
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/models", `{"provider":"custom","api_type":"openai","base_url":"http://127.0.0.1:9/v1","model":"local-m","alias":"loc"}`))
	if code != http.StatusOK || out["no_auth"] != true {
		t.Fatalf("keyless custom should be no_auth: %d %v", code, out)
	}

	// GET /api/models lists the new rows with the resolved fields.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/models", nil))
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	rows, _ := out["models"].([]any)
	var mmx map[string]any
	for _, r := range rows {
		if m, _ := r.(map[string]any); m["alias"] == "mmx" {
			mmx = m
		}
	}
	if mmx == nil || mmx["thinking"] != "on" || mmx["context_window"] != 1000000.0 {
		t.Fatalf("listed row missing advanced fields: %v", mmx)
	}
}

// TestAddModelKeepsCustomKeyOnEdit pins the upsert contract the registry's
// edit action leans on: re-submitting an existing alias without a key keeps
// the credential on file, so fixing a model id cannot silently turn a keyed
// relay into a keyless one. An explicit 免密钥 is a decision and still wins.
func TestAddModelKeepsCustomKeyOnEdit(t *testing.T) {
	h, _, _ := newEnginePanel(t)
	base := `{"provider":"custom","api_type":"openai","base_url":"http://127.0.0.1:9/v1"`

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		base+`,"model":"relay-a","api_key":"sk-keep","alias":"relay"}`))
	if code != http.StatusOK || out["key_set"] != true {
		t.Fatalf("add = %d %v", code, out)
	}

	// Same alias, new model id, key left blank: the key survives. Editing is
	// PUT now — POST always appends another account, so a repeated alias is a
	// second row, not an update of the first.
	code, out = doJSON(t, h, jsonReq(http.MethodPut, "/api/models/relay",
		base+`,"model":"relay-b","alias":"relay"}`))
	if code != http.StatusOK {
		t.Fatalf("edit = %d %v", code, out)
	}
	if out["model"] != "relay-b" || out["key_set"] != true || out["no_auth"] != nil {
		t.Fatalf("edit dropped the stored key: %v", out)
	}

	// Ticking 免密钥 clears it instead of inheriting.
	code, out = doJSON(t, h, jsonReq(http.MethodPut, "/api/models/relay",
		base+`,"model":"relay-b","alias":"relay","no_auth":true}`))
	if code != http.StatusOK || out["no_auth"] != true || out["key_set"] == true {
		t.Fatalf("explicit no_auth ignored: %d %v", code, out)
	}
}

// TestAddModelPlaintextEndpoint covers the escape hatch for a relay that only
// speaks http: the network guard refuses a non-loopback plaintext endpoint
// unless the request says so, and the consent is what persists — the endpoint
// has to keep working after a restart, not just in the request that added it.
func TestAddModelPlaintextEndpoint(t *testing.T) {
	h, _, configPath := newEnginePanel(t)
	const relay = `{"provider":"custom","api_type":"openai","base_url":"http://106.53.40.245:8080","model":"relay-m","api_key":"sk-x","alias":"relay"}`

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq(http.MethodPost, "/api/models", relay))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "allow_plaintext") {
		t.Fatalf("plaintext endpoint without consent = %d %q", rr.Code, rr.Body.String())
	}

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		strings.Replace(relay, `"alias":"relay"`, `"alias":"relay","allow_plaintext":true`, 1)))
	if code != http.StatusOK {
		t.Fatalf("add with consent = %d %v", code, out)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(saved), "allow_plaintext: true") {
		t.Fatalf("consent not persisted:\n%s", saved)
	}
}

func TestMCPSettings(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	// GET: engine present, command disabled by default.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/settings/mcp", nil))
	if code != http.StatusOK || out["command"] != "" {
		t.Fatalf("get mcp = %v", out)
	}

	// PUT with a binary that cannot exist: the spawn fails, 400, no swap.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/mcp", `{"command":"definitely-not-a-real-binary-xyz --flag"}`)); code != http.StatusBadRequest {
		t.Fatalf("bad command status = %d, want 400", code)
	}

	// PUT with an empty command disables MCP cleanly.
	if code, out := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/mcp", `{"command":""}`)); code != http.StatusOK {
		t.Fatalf("disable status = %d, out = %v", code, out)
	}
	if code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/settings/mcp", nil)); code != http.StatusOK || out["command"] != "" {
		t.Fatalf("get after disable = %v", out)
	}
}

// ---- app settings ----

func TestAppSettingsRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.WorkPath = tmp
	cfg.Injection.Model = config.InjectionModelAuto
	cfg.Approval.Mode = config.ApprovalModeOnRequest
	configPath := filepath.Join(tmp, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{
		Store:      newTestStore(t),
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})

	// GET reports the normalized defaults and the read-only sandbox path.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/settings/app", nil))
	if code != http.StatusOK {
		t.Fatalf("get status = %d", code)
	}
	if out["injection_model"] != config.InjectionModelAuto || out["approval_mode"] != config.ApprovalModeOnRequest {
		t.Fatalf("defaults wrong: %v", out)
	}
	if sandbox, ok := out["sandbox"].(map[string]any); !ok || sandbox["work_path"] != tmp {
		t.Fatalf("sandbox wrong: %v", out["sandbox"])
	}

	// PUT validates every policy group before touching anything.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"bad injection", `{"injection_model":"sometimes","approval_mode":"always","memory_limits":{"user":1,"memory":1,"project":1}}`},
		{"bad approval", `{"injection_model":"never","approval_mode":"yolo","memory_limits":{"user":1,"memory":1,"project":1}}`},
		{"zero limit", `{"injection_model":"never","approval_mode":"always","memory_limits":{"user":0,"memory":1,"project":1}}`},
		{"bad agent name", `{"injection_model":"never","approval_mode":"always","memory_limits":{"user":1,"memory":1,"project":1},"preferred_agents":["a/b"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/app", tc.body)); code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
		})
	}

	// A valid PUT persists to config.yaml and updates the live config.
	body := `{"injection_model":"never","preferred_agents":["codex","opencode"],"memory_limits":{"user":500,"memory":1000,"project":2000},"approval_mode":"always"}`
	code, out = doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/app", body))
	if code != http.StatusOK {
		t.Fatalf("put status = %d, out = %v", code, out)
	}
	if out["injection_model"] != "never" || out["approval_mode"] != "always" {
		t.Fatalf("put echo wrong: %v", out)
	}
	if cfg.Injection.Model != config.InjectionModelNever || cfg.Approval.Mode != config.ApprovalModeAlways {
		t.Fatalf("live config not updated: %+v %+v", cfg.Injection, cfg.Approval)
	}
	onDisk, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(onDisk), "never") {
		t.Fatalf("config file not updated: %q err %v", onDisk, err)
	}
}

// ---- skills approval ----

func TestSkillsApprovalFlow(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	for _, sk := range []*skills.Skill{
		{Name: "deploy-check", Description: "verify deploys", Scope: skills.ScopeGlobal, Status: skills.StatusPending, Body: "steps"},
		{Name: "old-flow", Description: "legacy flow", Scope: skills.ScopeGlobal, Status: skills.StatusPending},
	} {
		if err := store.Save(sk); err != nil {
			t.Fatal(err)
		}
	}
	h := New(Deps{Store: newTestStore(t), SkillStore: store, StaticDir: t.TempDir(), Token: testToken})

	// List shows both pending skills.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/skills", nil))
	var list []skillJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Status != string(skills.StatusPending) {
		t.Fatalf("list = %+v", list)
	}

	// Approve activates; approving again is refused (not pending anymore).
	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/approve", `{"name":"deploy-check"}`)); code != http.StatusOK || out["status"] != "approved" {
		t.Fatalf("approve = %d %v", code, out)
	}
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/approve", `{"name":"deploy-check"}`)); code != http.StatusInternalServerError {
		t.Fatalf("double approve status = %d, want 500", code)
	}

	// Reject deletes the pending skill from the store.
	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/reject", `{"name":"old-flow"}`)); code != http.StatusOK || out["status"] != "rejected" {
		t.Fatalf("reject = %d %v", code, out)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/skills", nil))
	list = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Status != string(skills.StatusActive) {
		t.Fatalf("after approve/reject list = %+v", list)
	}

	// Input validation and unknown skills.
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"empty name", http.MethodPost, "/api/skills/approve", `{"name":""}`, http.StatusBadRequest},
		{"invalid json", http.MethodPost, "/api/skills/approve", "{not json", http.StatusBadRequest},
		{"unknown skill", http.MethodPost, "/api/skills/approve", `{"name":"ghost"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := doJSON(t, h, jsonReq(tc.method, tc.path, tc.body)); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

// Without a skill store the routes are not even registered: fail closed.
func TestSkillsUnconfigured(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/skills", nil)); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route unregistered)", code)
	}
}

func TestSkillsHubAndImportAPI(t *testing.T) {
	store := skills.NewStore(t.TempDir())
	h := New(Deps{Store: newTestStore(t), SkillStore: store, StaticDir: t.TempDir(), Token: testToken})

	// 1. GET /api/skills/hub
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/skills/hub?q=git", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/skills/hub code = %d", rr.Code)
	}
	var hubList []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &hubList); err != nil {
		t.Fatalf("unmarshal hub list: %v", err)
	}
	if len(hubList) == 0 {
		t.Fatalf("expected at least 1 matching hub skill for 'git'")
	}
	if hubList[0]["installed"].(bool) {
		t.Errorf("expected installed to be false initially")
	}

	// 2. POST /api/skills/hub/install
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/hub/install", `{"name":"git-workflow"}`))
	if code != http.StatusOK || out["status"] != "installed" {
		t.Fatalf("install hub skill failed: %d %v", code, out)
	}

	// 3. Verify installed status in hub listing
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/skills/hub?q=git-workflow", nil))
	hubList = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &hubList)
	if len(hubList) == 0 || !hubList[0]["installed"].(bool) {
		t.Fatalf("expected installed=true after install, got %+v", hubList)
	}

	// 4. POST /api/skills/import with raw content
	content := `{"content":"---\nname: api-skill\ndescription: imported via api\n---\nBody"}`
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/import", content))
	if code != http.StatusOK || out["status"] != "imported" {
		t.Fatalf("import skill failed: %d %v", code, out)
	}

	// 5. Check GET /api/skills
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/skills", nil))
	var localList []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &localList)
	found := false
	for _, item := range localList {
		if item["name"] == "api-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("api-skill not found in GET /api/skills")
	}

	// 6. POST /api/skills/discover
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/skills/discover", `{"query":"docker"}`))
	if code != http.StatusOK || out["name"] != "docker-compose" {
		t.Fatalf("discover skill failed: %d %v", code, out)
	}
}

// ---- reminders ----

func TestRemindersCRUD(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Reminders: reminders.NewStore(newMigratedDB(t)),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"invalid json", "{not json", http.StatusBadRequest},
		{"empty message", `{"message":""}`, http.StatusBadRequest},
		{"no time", `{"message":"x"}`, http.StatusBadRequest},
		{"bad due_at", `{"message":"x","due_at":"tomorrow"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/reminders", tc.body)); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}

	// Create with an explicit due_at; source records the web origin.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/reminders", `{"message":"standup","due_at":"2026-09-01T09:00:00+08:00"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, out = %v", code, out)
	}
	if id, _ := out["id"].(float64); id <= 0 {
		t.Fatalf("no id assigned: %v", out)
	}
	if out["source"] != "web" {
		t.Fatalf("source = %v, want web", out["source"])
	}

	// List shows it; delete round-trips; deleting again is 404.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/reminders", nil))
	var list []reminders.Reminder
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err %v", list, err)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/reminders/1", nil)); code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/reminders/1", nil)); code != http.StatusNotFound {
		t.Fatalf("re-delete status = %d, want 404", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/reminders/abc", nil)); code != http.StatusBadRequest {
		t.Fatalf("non-numeric id status = %d, want 400", code)
	}
}

// ---- system endpoints ----

func TestSystemEndpoints(t *testing.T) {
	db := newMigratedDB(t)
	h := New(Deps{Store: newTestStore(t), DB: db, StaticDir: t.TempDir(), Token: testToken})

	// Version mirrors the internal version string and build provenance.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/version", nil))
	if code != http.StatusOK || out["version"] != versionpkg.Version || out["provenance"] != versionpkg.ProvenanceClass() {
		t.Fatalf("version = %d %v", code, out)
	}

	// Metrics: empty list is a valid 200.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/metrics", nil))
	var metrics []any
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &metrics) != nil || len(metrics) != 0 {
		t.Fatalf("metrics = %d %s", rr.Code, rr.Body.String())
	}

	// Audit verification: an empty chain verifies, per-task and global.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/audit?task_id=nope", nil))
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("task audit = %d %v", code, out)
	}
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/audit", nil))
	if code != http.StatusOK || out["ok"] != true || out["entries"] != float64(0) {
		t.Fatalf("global audit = %d %v", code, out)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/audit/entries", nil))
	var entries []any
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &entries) != nil || len(entries) != 0 {
		t.Fatalf("audit entries = %d %s", rr.Code, rr.Body.String())
	}

	// Without the db handle audit endpoints fail closed with 503.
	bare := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	if code, _ := doJSON(t, bare, authedReq(http.MethodGet, "/api/audit", nil)); code != http.StatusServiceUnavailable {
		t.Fatalf("audit without db status = %d, want 503", code)
	}
	if code, _ := doJSON(t, bare, authedReq(http.MethodGet, "/api/audit/entries", nil)); code != http.StatusServiceUnavailable {
		t.Fatalf("audit entries without db status = %d, want 503", code)
	}
}

// ---- self profile ----

func TestSelfProfile(t *testing.T) {
	cfg := config.Default()
	cfg.Node.Name = "panda-test-node"
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/self", nil))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out["os"] != runtime.GOOS || out["arch"] != runtime.GOARCH {
		t.Fatalf("os/arch wrong: %v", out)
	}
	if cores, _ := out["cpu_cores"].(float64); cores <= 0 {
		t.Fatalf("cpu_cores = %v", out["cpu_cores"])
	}
	if out["hostname"] == "" || out["node_name"] != "panda-test-node" {
		t.Fatalf("identity wrong: %v", out)
	}
	if out["node"] != nil { // no db wired: the ledger summary is omitted
		t.Fatalf("node = %v, want nil without db", out["node"])
	}
}

// ---- agent probing ----

func TestAgentProbeAndTest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI scripts are POSIX-only")
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'claude 9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin) // only the fake claude resolves; codex/opencode don't

	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})

	// List: claude_code installed with the probed version, others missing.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/agents", nil))
	var agents []agentJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &agents); err != nil || len(agents) != len(agentreg.Registry()) {
		t.Fatalf("agents = %s err %v", rr.Body.String(), err)
	}
	found := map[string]agentJSON{}
	for _, a := range agents {
		found[a.Name] = a
	}
	if !found["claude_code"].Installed || found["claude_code"].Version != "claude 9.9.9" {
		t.Fatalf("claude_code = %+v", found["claude_code"])
	}
	if found["codex"].Installed {
		t.Fatalf("codex should be missing from the fake PATH: %+v", found["codex"])
	}

	// Test: the installed fake CLI answers; a missing one reports ok:false;
	// an unknown name is a 404.
	code, out := doJSON(t, h, authedReq(http.MethodPost, "/api/agents/claude_code/test", nil))
	if code != http.StatusOK || out["ok"] != true || out["version"] != "claude 9.9.9" {
		t.Fatalf("claude test = %d %v", code, out)
	}
	code, out = doJSON(t, h, authedReq(http.MethodPost, "/api/agents/codex/test", nil))
	if code != http.StatusOK || out["ok"] != false {
		t.Fatalf("codex test = %d %v", code, out)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodPost, "/api/agents/ghost/test", nil)); code != http.StatusNotFound {
		t.Fatalf("unknown agent status = %d, want 404", code)
	}
}

func TestAgentListCache(t *testing.T) {
	t.Run("reuses fresh results and honors expiry and force refresh", func(t *testing.T) {
		var cache agentListCache
		calls := 0
		probe := func() []agentJSON {
			calls++
			return []agentJSON{{Name: "agent", Version: strings.Repeat("v", calls)}}
		}
		if got := cache.get(false, probe); got[0].Version != "v" {
			t.Fatalf("first probe = %+v", got)
		}
		if got := cache.get(false, probe); got[0].Version != "v" || calls != 1 {
			t.Fatalf("cached probe = %+v, calls = %d", got, calls)
		}
		if got := cache.get(true, probe); got[0].Version != "vv" || calls != 2 {
			t.Fatalf("forced probe = %+v, calls = %d", got, calls)
		}
		cache.expires = cache.expires.Add(-agentListCacheTTL)
		if got := cache.get(false, probe); got[0].Version != "vvv" || calls != 3 {
			t.Fatalf("expired probe = %+v, calls = %d", got, calls)
		}
	})

	t.Run("coalesces concurrent cold reads", func(t *testing.T) {
		var cache agentListCache
		calls := 0
		started := make(chan struct{})
		release := make(chan struct{})
		probe := func() []agentJSON {
			calls++
			close(started)
			<-release
			return []agentJSON{{Name: "agent"}}
		}
		results := make(chan []agentJSON, 8)
		for range 8 {
			go func() { results <- cache.get(false, probe) }()
		}
		<-started
		close(release)
		for range 8 {
			if got := <-results; len(got) != 1 || got[0].Name != "agent" {
				t.Fatalf("concurrent result = %+v", got)
			}
		}
		if calls != 1 {
			t.Fatalf("probe calls = %d, want 1", calls)
		}
	})
}

// TestRemoveNode covers DELETE /api/nodes/{id}: a stale offline remote is
// removed; the local node's own row is refused (its heartbeat re-registers
// it); an online node is refused (its next hello re-registers it); an
// unknown id is a 404.
func TestRemoveNode(t *testing.T) {
	db := newMigratedDB(t)
	cfg := config.Default()
	h := New(Deps{Store: newTestStore(t), DB: db, Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})

	// Two stale offline rows — the pre-identity-fix duplicates — and one
	// live remote.
	if err := ledger.Register(db, ledger.Card{Device: "stale-1"}, "stale-1", 1); err != nil {
		t.Fatalf("register stale-1: %v", err)
	}
	if err := ledger.MarkOffline(db, "stale-1"); err != nil {
		t.Fatalf("offline stale-1: %v", err)
	}
	if err := ledger.Register(db, ledger.Card{Device: "stale-2"}, "stale-2", 1); err != nil {
		t.Fatalf("register stale-2: %v", err)
	}
	if err := ledger.MarkOffline(db, "stale-2"); err != nil {
		t.Fatalf("offline stale-2: %v", err)
	}
	if err := ledger.UpsertRemote(db, "live-pi", ledger.CapabilitySummary{Device: "live-pi"}); err != nil {
		t.Fatalf("upsert live-pi: %v", err)
	}

	// Offline remote: removed.
	code, out := doJSON(t, h, authedReq(http.MethodDelete, "/api/nodes/stale-1", nil))
	if code != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove stale-1 = %d %v", code, out)
	}
	nodes, err := ledger.Query(db, "offline", "")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("expected only stale-2 left, got %d nodes (err %v)", len(nodes), err)
	}

	// Online remote: refused — a live peer re-registers itself.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/nodes/live-pi", nil)); code != http.StatusConflict {
		t.Fatalf("online remove = %d, want 409", code)
	}

	// The local node's own row: refused.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/nodes/"+localNodeID(cfg), nil)); code != http.StatusBadRequest {
		t.Fatalf("self remove = %d, want 400", code)
	}

	// Unknown id: 404.
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/nodes/ghost", nil)); code != http.StatusNotFound {
		t.Fatalf("unknown remove = %d, want 404", code)
	}
}
