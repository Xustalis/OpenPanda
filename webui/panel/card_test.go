package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"gopkg.in/yaml.v3"
)

// seedCard writes a minimal valid capabilities.yaml (with a comment, so the
// comment-preservation contract is exercised) and returns its path.
func seedCard(t *testing.T, dir string) string {
	t.Helper()
	card := `# this node's advertised abilities
device: test-node
resource_class: laptop
chip: M1
native:
  - id: uname
    command: uname
    args: ["-a"]
    tier: 1
    description: system summary
agents:
  opencode:
    adapter: scripts/opencode.sh
    capabilities: [code]
    best_at: [refactor]
    tier: 2
manual:
  - id: charge-battery
    notify: "email ops"
`
	path := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	return path
}

// newCardPanel builds a panel wired with a card file and a seeded config, so
// the card endpoints (and nodes/add's config write) run against real files.
func newCardPanel(t *testing.T) (http.Handler, *config.Config, string, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DBPath = filepath.Join(tmp, "panel.db")
	cfg.Storage.MemoryPath = filepath.Join(tmp, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(tmp, "projects")
	cfg.Storage.SkillsPath = filepath.Join(tmp, "skills")
	cfg.Storage.WorkPath = tmp
	cfg.Model.BaseURL = "" // answers-only: the card API needs no engine
	configPath := filepath.Join(tmp, "config.yaml")
	if data, err := yaml.Marshal(cfg); err != nil {
		t.Fatalf("marshal config: %v", err)
	} else if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cardPath := seedCard(t, tmp)
	h := New(Deps{
		Store:      newTestStore(t),
		Cfg:        cfg,
		ConfigPath: configPath,
		CardPath:   cardPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, cfg, configPath, cardPath
}

// TestCardReadAndMutations walks the whole card API: read, add/tier/remove an
// agent, add/remove a native ability, add/remove a manual ability — and
// verifies each write landed in the file, kept the untouched comment, and
// left a .bak behind.
func TestCardReadAndMutations(t *testing.T) {
	h, _, _, cardPath := newCardPanel(t)

	// Read: parsed form + raw YAML + path.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/card", nil))
	if code != http.StatusOK {
		t.Fatalf("get status = %d", code)
	}
	if out["path"] != cardPath {
		t.Errorf("path = %v", out["path"])
	}
	if raw, _ := out["raw"].(string); !strings.Contains(raw, "# this node's advertised abilities") {
		t.Errorf("raw must carry the file text incl. comments")
	}
	card, _ := out["card"].(map[string]any)
	if card == nil || card["device"] != "test-node" {
		t.Fatalf("card = %v", out["card"])
	}

	// Agent add.
	body := `{"adapter":"scripts/codex.sh","tier":2,"capabilities":["code","review"]}`
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/card/agents/codex", body))
	if code != http.StatusOK {
		t.Fatalf("agent add status = %d body %v", code, out)
	}
	// Duplicate add is an error, not a silent overwrite.
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/card/agents/codex", body))
	if code != http.StatusBadRequest {
		t.Errorf("duplicate agent add status = %d, want 400", code)
	}

	// Agent patch: tier toggle only — capabilities/best_at must survive.
	code, out = doJSON(t, h, jsonReq(http.MethodPatch, "/api/card/agents/codex", `{"tier":1}`))
	if code != http.StatusOK {
		t.Fatalf("agent patch status = %d body %v", code, out)
	}
	data, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tier: 1") {
		t.Errorf("tier 1 not written:\n%s", data)
	}
	if !strings.Contains(string(data), "scripts/codex.sh") {
		t.Errorf("adapter lost in patch:\n%s", data)
	}
	if !strings.Contains(string(data), "# this node's advertised abilities") {
		t.Errorf("comment lost in patch:\n%s", data)
	}
	if _, err := os.Stat(cardPath + ".bak"); err != nil {
		t.Errorf(".bak missing after write: %v", err)
	}

	// Agent remove.
	code, _ = doJSON(t, h, authedReq(http.MethodDelete, "/api/card/agents/codex", nil))
	if code != http.StatusOK {
		t.Fatalf("agent remove status = %d", code)
	}
	// Remove a missing agent: 404.
	code, _ = doJSON(t, h, authedReq(http.MethodDelete, "/api/card/agents/codex", nil))
	if code != http.StatusNotFound {
		t.Errorf("missing agent remove status = %d, want 404", code)
	}

	// Native add (missing command must be refused).
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/card/native", `{"id":"df","command":"df","args":["-h"],"tier":1}`))
	if code != http.StatusOK {
		t.Fatalf("native add status = %d", code)
	}
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/card/native", `{"id":"nope"}`))
	if code != http.StatusBadRequest {
		t.Errorf("native add without command status = %d, want 400", code)
	}
	// Native remove.
	code, _ = doJSON(t, h, authedReq(http.MethodDelete, "/api/card/native/df", nil))
	if code != http.StatusOK {
		t.Fatalf("native remove status = %d", code)
	}

	// Manual add + remove.
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/card/manual", `{"id":"water-plants","notify":"email ops"}`))
	if code != http.StatusOK {
		t.Fatalf("manual add status = %d", code)
	}
	code, _ = doJSON(t, h, authedReq(http.MethodDelete, "/api/card/manual/water-plants", nil))
	if code != http.StatusOK {
		t.Fatalf("manual remove status = %d", code)
	}
}

// TestPutCardRawValidatesBeforeWrite: an invalid raw replacement (empty
// device) is refused and the file on disk is untouched.
func TestPutCardRawValidatesBeforeWrite(t *testing.T) {
	h, _, _, cardPath := newCardPanel(t)
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	// yamlBody builds a properly-escaped {"yaml": ...} JSON body — multi-line
	// YAML cannot be concatenated into a JSON string literal by hand.
	yamlBody := func(yamlText string) string {
		t.Helper()
		b, err := json.Marshal(map[string]string{"yaml": yamlText})
		if err != nil {
			t.Fatalf("marshal yaml body: %v", err)
		}
		return string(b)
	}

	req := jsonReq(http.MethodPut, "/api/card", yamlBody("device: \nresource_class: laptop\n"))
	rr := authedReqWriter(t, h, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid raw status = %d, want 400", rr.Code)
	}
	after, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file changed on a refused write")
	}

	// A valid replacement round-trips.
	valid := "device: test-node\nresource_class: laptop\nnative: []\nagents: {}\nmanual: []\n"
	code, _ := doJSON(t, h, jsonReq(http.MethodPut, "/api/card", yamlBody(valid)))
	if code != http.StatusOK {
		t.Fatalf("valid raw status = %d", code)
	}
	got, _ := os.ReadFile(cardPath)
	if !strings.Contains(string(got), "device: test-node") {
		t.Errorf("raw replacement not written: %s", got)
	}
}

// TestCardEndpointsWithoutCard: no CardPath wired and no engine → the card
// endpoints degrade to 503 rather than panic.
func TestCardEndpointsWithoutCard(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})
	for _, target := range []string{"/api/card"} {
		code, _ := doJSON(t, h, authedReq(http.MethodGet, target, nil))
		if code != http.StatusServiceUnavailable {
			t.Errorf("GET %s without card status = %d, want 503", target, code)
		}
	}
	code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/card/native", `{"id":"x","command":"y"}`))
	if code != http.StatusServiceUnavailable {
		t.Errorf("POST without card status = %d, want 503", code)
	}
}

// TestNodesAddWritesConfigAndDials: the join-a-device endpoint appends the
// peer to config.yaml (generating a shared secret when missing) and reports
// the join guide. The live dial is skipped (no engine), which the response
// must say honestly.
func TestNodesAddWritesConfigAndDials(t *testing.T) {
	h, cfg, configPath, _ := newCardPanel(t)
	if cfg.Network.SharedSecret != "" {
		t.Fatal("test premise: default config has no shared secret")
	}

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add", `{"addr":"192.168.1.20:7836"}`))
	if code != http.StatusOK {
		t.Fatalf("nodes add status = %d body %v", code, out)
	}
	if out["added"] != true || out["secret_generated"] != true {
		t.Errorf("added/secret_generated = %v/%v", out["added"], out["secret_generated"])
	}
	if out["dialed"] != false {
		t.Errorf("dialed must be false without an engine: %v", out["dialed"])
	}
	steps, _ := out["invite_steps"].([]any)
	if len(steps) != 3 {
		t.Errorf("invite_steps = %v, want 3", out["invite_steps"])
	}

	// The peer (and the generated secret) landed in the file, and the live
	// config copy is in sync so a follow-up add sees it.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "192.168.1.20:7836") {
		t.Errorf("peer not written to config:\n%s", data)
	}
	if !strings.Contains(string(data), "shared_secret:") {
		t.Errorf("shared secret not written to config:\n%s", data)
	}
	if cfg.Network.SharedSecret == "" {
		t.Errorf("live config not synced with the generated secret")
	}

	// Adding the same peer again is not an error — the row is already there.
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add", `{"addr":"192.168.1.20:7836"}`))
	if code != http.StatusOK || out["added"] != false {
		t.Fatalf("re-add status = %d added=%v", code, out["added"])
	}

	// A malformed address is a 400, not a panic.
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add", `{"addr":"no-port-here"}`))
	if code != http.StatusBadRequest {
		t.Errorf("bad addr status = %d, want 400", code)
	}
}

// authedReqWriter runs req through the handler and returns the recorder (for
// handlers whose body is not a JSON object).
func authedReqWriter(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}
