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
	cfg := config.Default()
	cfg.Storage.DBPath = filepath.Join(tmp, "engine.db")
	cfg.Storage.MemoryPath = filepath.Join(tmp, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(tmp, "projects")
	cfg.Storage.SkillsPath = filepath.Join(tmp, "skills")
	cfg.Storage.WorkPath = tmp
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
	if !got.APIKeySet || got.APIKeyHint != "…1234" {
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

	// Version mirrors the internal version string.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/version", nil))
	if code != http.StatusOK || out["version"] != versionpkg.Version {
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
