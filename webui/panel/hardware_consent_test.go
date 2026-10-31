// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestHardwareAskUsesPanelAuthentication(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	for _, path := range []string{"/api/hardware/ask", "/api/ask"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"prompt":"fixture","authorize":true}`)))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, rr.Code)
		}
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, path, strings.NewReader(`{"prompt":""}`)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s: %d", path, rr.Code)
		}
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, path, strings.NewReader(`{"prompt":"fixture"}`)))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing engine %s: %d", path, rr.Code)
		}
	}
}

func newHardwarePlanAPIEngine(t *testing.T) *askengine.Engine {
	t.Helper()
	dir := t.TempDir()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A granted task only runs this test binary with an empty test selection;
	// there is no shell command, machine dependency or external service.
	card := fmt.Sprintf("device: hardware-plan-test\nnative:\n  - id: fixture:harmless\n    command: %q\n    args: [\"-test.run=^$\"]\n    tier: 2\ncapacity:\n  cpu_cores: 4\n  ram_gb: 8\n  max_concurrent_tasks: 2\n", program)
	cardPath := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(cardPath, []byte(card), 0o600); err != nil {
		t.Fatal(err)
	}
	directive := map[string]any{"kind": "plan", "plan": map[string]any{
		"goal": "harmless two-stage fixture", "stages": []map[string]any{
			{"id": "first", "intent": "first fixture", "requires": []string{"fixture:harmless"}, "needs": []string{}},
			{"id": "later", "intent": "later fixture", "requires": []string{"fixture:harmless"}, "needs": []string{"first"}},
		},
	}}
	text, err := json.Marshal(directive)
	if err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	}))
	t.Cleanup(model.Close)
	cfg := &config.Config{
		Node:     config.NodeConfig{Name: "hardware-plan-test", Kind: "vm", ResourceClass: "Standard"},
		Storage:  config.StorageConfig{DBPath: filepath.Join(dir, "test.db"), MemoryPath: filepath.Join(dir, "memory"), ProjectsPath: filepath.Join(dir, "projects"), SkillsPath: filepath.Join(dir, "skills"), WorkPath: dir},
		Model:    config.ModelConfig{BaseURL: model.URL, APIKey: "test", Model: "test"},
		Approval: config.ApprovalConfig{Mode: config.ApprovalModeOnRequest},
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{CardPath: cardPath, QueueTasks: true, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func TestHardwareAskPlanConsentIsRequestScoped(t *testing.T) {
	for _, tc := range []struct {
		name, path  string
		grant, want bool
	}{
		{"hardware with grant", "/api/hardware/ask", true, true},
		{"hardware without grant", "/api/hardware/ask", false, false},
		{"ordinary with grant unchanged", "/api/ask", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newHardwarePlanAPIEngine(t)
			h := New(Deps{Store: engine.TaskStore(), Engine: engine, StaticDir: t.TempDir(), Token: testToken})
			rr := httptest.NewRecorder()
			req := authedReq(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(`{"prompt":"run harmless fixture","authorize":%v}`, tc.grant)))
			// An untrusted marker header on /api/ask must not change its behavior.
			req.Header.Set("X-Panda-Hardware-Origin", "true")
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("ask: %d %s", rr.Code, rr.Body.String())
			}
			var out struct {
				Kind   string `json:"kind"`
				PlanID string `json:"plan_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Kind != "plan" || out.PlanID == "" {
				t.Fatalf("response: %+v %v %s", out, err, rr.Body.String())
			}
			stages, err := engine.PlanStages(context.Background(), out.PlanID)
			if err != nil || len(stages) != 2 {
				t.Fatalf("stages: %+v %v", stages, err)
			}
			for _, stage := range stages {
				if stage.Authorized != tc.want {
					t.Fatalf("stage %s authorized=%v want %v", stage.StageID, stage.Authorized, tc.want)
				}
			}
		})
	}
}
