// SPDX-License-Identifier: AGPL-3.0-or-later

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
)

func TestExecutionSettingsRoundTripAndValidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("node:\n  name: execution-settings\nrouting:\n  tools_policy: extended # preserve this\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, ConfigPath: path, StaticDir: root, Token: testToken})
	body := `{"agent_models":{"codex":["parent-model"]},"subagent_models":{"codex":["child-model"]},"max_concurrent":6,"agent_concurrency":{"codex":2},"delegation":{"enabled":true,"max_concurrent":3,"max_children":12,"fanout":4,"max_depth":2}}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/settings/execution", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Routing.MaxConcurrent != 6 || loaded.Routing.SubagentModels["codex"][0] != "child-model" || loaded.Routing.ToolsPolicy != config.ToolsPolicyExtended || loaded.Delegation.MaxConcurrent != 3 {
		t.Fatalf("persisted config: %+v", loaded.RuntimeConfig())
	}
	before, _ := os.ReadFile(path)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/settings/execution", nil))
	var out executionSettingsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.AgentConcurrency["codex"] != 2 {
		t.Fatalf("get: %s %v", rr.Body.String(), err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/settings/execution", strings.NewReader(strings.Replace(body, `"max_concurrent":6`, `"max_concurrent":-1`, 1))))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit accepted: %d", rr.Code)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid request changed persisted policy")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/execution", strings.NewReader(body)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write: %d", rr.Code)
	}
}
