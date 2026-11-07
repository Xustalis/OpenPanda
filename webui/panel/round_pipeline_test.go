// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// Real panel ask -> fake model tool batch -> durable queue -> native adapter
// -> session finalization. No vendor, API key, or user daemon is involved.
func TestRoundPipelineBatchedToolsExecuteAndSummarize(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	var calls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		emit := func(v any) { raw, _ := json.Marshal(v); _, _ = fmt.Fprintf(w, "data: %s\n\n", raw) }
		emit(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]int{"input_tokens": 10}}})
		if calls.Add(1) == 1 {
			for i, title := range []string{"first native job", "second native job"} {
				args, _ := json.Marshal(map[string]any{"title": title, "target": "run " + title, "abilities": []string{"test:once"}})
				emit(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("task-%d", i), "name": "task_submit"}})
				emit(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
				emit(map[string]any{"type": "content_block_stop", "index": i})
			}
		} else {
			emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}})
			emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Both tasks have been queued."}})
			emit(map[string]any{"type": "content_block_stop", "index": 0})
		}
		emit(map[string]any{"type": "message_stop"})
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{APIType: config.APITypeAnthropic, BaseURL: model.URL, Model: "pipeline-test", NoAuth: true}); err != nil {
		t.Fatal(err)
	}
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("batched native jobs")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: sessStore, cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(), activeAsks: map[string]sessionOperation{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask", strings.NewReader(`{"prompt":"Run two native jobs","authorize":true}`)))
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "event: error") {
		t.Fatalf("ask failed: %d %s", rr.Code, rr.Body.String())
	}
	tasks, err := h.store.ListBySession(context.Background(), sess.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("batch/session linkage: %d %v; stream %s", len(tasks), err, rr.Body.String())
	}
	for _, task := range tasks {
		waitTaskState(t, h.store, task.TaskID, core.StateDone)
	}
	h.finalizeSessionSweep(context.Background())
	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, turn := range got.Turns {
		if turn.RoundID != "" {
			count++
			if !strings.Contains(turn.Text, "2 completed") {
				t.Fatalf("incomplete round: %s", turn.Text)
			}
		}
	}
	if count != 1 {
		t.Fatalf("round summaries=%d; turns=%+v", count, got.Turns)
	}
	raw, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(raw), "x\n") != 2 {
		t.Fatalf("native commands did not execute exactly twice: %q %v", raw, err)
	}
}

func TestProviderOnlyModelSettingsPreserveExistingCredential(t *testing.T) {
	cfg := config.Default()
	cfg.Model = config.ModelConfig{Provider: "deepseek", Model: "first", APIKey: "fake-existing-key"}
	h := New(Deps{Store: newTestStore(t), Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})
	code, out := doJSON(t, h, jsonReq(http.MethodPut, "/api/settings/model", `{"provider":"deepseek","model":"second"}`))
	if code != http.StatusOK || out["model"] != "second" || cfg.Model.APIKey != "fake-existing-key" {
		t.Fatalf("provider-only save: %d %+v", code, out)
	}
	if strings.Contains(out["base_url"].(string), "fake-existing-key") {
		t.Fatal("secret leak")
	}
}
