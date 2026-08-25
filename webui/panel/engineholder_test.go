package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"gopkg.in/yaml.v3"
)

// newHolderConfig builds a config wired to temp storage, ready for engine
// construction without dialing anything (the model endpoint, once set,
// points at a port nothing listens on).
func newHolderConfig(t *testing.T) *config.Config {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DBPath = filepath.Join(tmp, "engine.db")
	cfg.Storage.MemoryPath = filepath.Join(tmp, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(tmp, "projects")
	cfg.Storage.SkillsPath = filepath.Join(tmp, "skills")
	cfg.Storage.WorkPath = tmp
	return cfg
}

// TestEngineHolderHotLoad covers the zero-config lifecycle of the reloadable
// engine holder: start with no model (nil engine, degraded mode), configure
// one and Reload (engine live), swap the engine under concurrent readers,
// and verify a failed rebuild leaves the previous engine serving.
func TestEngineHolderHotLoad(t *testing.T) {
	cfg := newHolderConfig(t)
	cfg.Model.BaseURL = ""

	// Zero-config start: degraded mode, no error, engine nil — `panda web`
	// boots exactly like this.
	h, err := NewEngineHolder(cfg, askengine.Options{})
	if err != nil {
		t.Fatalf("NewEngineHolder: %v", err)
	}
	defer h.Close()
	if eng := h.Engine(); eng != nil {
		t.Fatal("engine must be nil before a model is configured")
	}

	// First model configured (dead endpoint — construction must succeed
	// without dialing): Reload brings the engine up.
	cfg.Model.BaseURL = "http://127.0.0.1:9"
	cfg.Model.Model = "hot-model"
	if err := h.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	first := h.Engine()
	if first == nil {
		t.Fatal("engine must be live after the first configured reload")
	}

	// Hammer Engine() while a reload swaps the pointer: once non-nil it must
	// never regress to nil, and the swap itself must be race-free (verified
	// under -race).
	cfg.Model.BaseURL = "http://127.0.0.1:10"
	cfg.Model.Model = "second-model"
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if h.Engine() == nil {
					t.Error("engine regressed to nil during reload")
					return
				}
			}
		}()
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	close(stop)
	wg.Wait()

	second := h.Engine()
	if second == nil || second == first {
		t.Fatal("reload must swap in a new engine")
	}
	if mc := second.ModelConfig(); mc.Model != "second-model" || mc.BaseURL != "http://127.0.0.1:10" {
		t.Fatalf("new engine built from stale config: %+v", mc)
	}

	// A failed rebuild (non-https, non-loopback endpoints are rejected by
	// the network guard) keeps the previous engine serving.
	cfg.Model.BaseURL = "http://model.invalid.example"
	if err := h.Reload(); err == nil {
		t.Fatal("reload must fail on an invalid endpoint")
	}
	if got := h.Engine(); got != second {
		t.Fatal("previous engine must keep serving after a failed reload")
	}

	// Tear-down: Close releases the engine; the holder is back to degraded.
	h.Close()
	if eng := h.Engine(); eng != nil {
		t.Fatal("engine must be nil after Close")
	}
}

// TestModelSettingsHotLoadEngine drives the onboarding path end to end over
// HTTP: a zero-config panel answers /api/ask with 503, the connectivity test
// works without a live engine, and the first model save hot-loads the engine
// immediately — no process restart.
func TestModelSettingsHotLoadEngine(t *testing.T) {
	cfg := newHolderConfig(t)
	cfg.Model.BaseURL = ""
	holder, err := NewEngineHolder(cfg, askengine.Options{})
	if err != nil {
		t.Fatalf("NewEngineHolder: %v", err)
	}
	t.Cleanup(holder.Close)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{
		Store:        newTestStore(t),
		EngineHolder: holder,
		Cfg:          cfg,
		ConfigPath:   configPath,
		StaticDir:    t.TempDir(),
		Token:        testToken,
	})

	// Unconfigured: ask is 503, GET reports an empty base_url.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/ask", `{"prompt":"hi"}`)); code != http.StatusServiceUnavailable {
		t.Fatalf("ask before config status = %d, want 503", code)
	}
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/settings/model", nil))
	if code != http.StatusOK || out["base_url"] != "" {
		t.Fatalf("get before config = %d %v", code, out)
	}

	// The connectivity test works without a live engine (a dead endpoint
	// reports ok:false, not a 5xx) — the onboarding form relies on it to
	// verify a candidate provider before the first save.
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/settings/model/test", `{"base_url":"http://127.0.0.1:9","model":"m"}`))
	if code != http.StatusOK || out["ok"] != false {
		t.Fatalf("test without engine = %d %v, want 200 ok:false", code, out)
	}

	// First save: persists, hot-loads the engine, and reports it back.
	body := `{"api_type":"anthropic","base_url":"http://127.0.0.1:9","model":"boot-model"}`
	code, out = doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/model", body))
	if code != http.StatusOK {
		t.Fatalf("put status = %d, out = %v", code, out)
	}
	if out["model"] != "boot-model" {
		t.Fatalf("put echo wrong: %v", out)
	}
	eng := holder.Engine()
	if eng == nil {
		t.Fatal("engine must hot-load on the first model save")
	}
	if mc := eng.ModelConfig(); mc.Model != "boot-model" || mc.BaseURL != "http://127.0.0.1:9" {
		t.Fatalf("hot-loaded engine config wrong: %+v", mc)
	}

	// GET reflects the new state; ask no longer reports "not configured"
	// (the dead endpoint surfaces as a plain 500 instead).
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/settings/model", nil))
	if code != http.StatusOK || out["base_url"] != "http://127.0.0.1:9" || out["model"] != "boot-model" {
		t.Fatalf("get after hot-load = %v", out)
	}
	// No API key was saved: the ask surfaces the missing-key configuration
	// gap as 503 (same family as "not configured"), not a server-fault 500.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/ask", `{"prompt":"hi"}`)); code != http.StatusServiceUnavailable {
		t.Fatalf("ask after hot-load status = %d, want 503 (engine live, key missing)", code)
	}

	// The config file on disk carries the model for the next start.
	onDisk, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(onDisk), "boot-model") {
		t.Fatalf("config file not updated: %q err %v", onDisk, err)
	}
}
