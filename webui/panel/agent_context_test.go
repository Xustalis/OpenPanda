// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// The window comes from the provider catalogue, not from a guess: the ids a
// CLI reports are vendor ids, and a name the catalogue does not know must
// resolve to 0 so the gauge prints the count alone.
func TestContextWindowForModel(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-sonnet-4-5-20250929", 200_000},
		{"claude-sonnet-4-6", 1_000_000},
		{"claude-opus-4-5", 200_000},
		{"", 0},
		{"totally-unknown-model", 0},
	}
	for _, c := range cases {
		if got := contextWindowForModel(c.model); got != c.want {
			t.Errorf("contextWindowForModel(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

// A context sample is gauge metadata, not a status line: the list must carry
// the newest one (per task) and must never let it become last_event.
func TestListTasksAttachesAgentContext(t *testing.T) {
	db := testdb.Open(t)
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	h := New(Deps{Store: store, DB: db, StaticDir: t.TempDir(), Token: testToken})

	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "context task", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvAgentContext,
		map[string]any{"adapter": "claude_code.py", "context_tokens": 12_000, "model": "claude-sonnet-4-5"}); err != nil {
		t.Fatalf("record first sample: %v", err)
	}
	// A progress row lands between the two samples: it must stay the newest
	// *visible* event even after a fresher context sample arrives.
	if err := store.RecordEvent(ctx, task.TaskID, "progress",
		map[string]any{"note": "Bash: go test"}); err != nil {
		t.Fatalf("record progress: %v", err)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvAgentContext,
		map[string]any{"adapter": "claude_code.py", "context_tokens": 48_500, "model": "claude-sonnet-4-5"}); err != nil {
		t.Fatalf("record second sample: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/api/tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var tasks []struct {
		ID            string `json:"id"`
		ContextTokens int64  `json:"context_tokens"`
		ContextModel  string `json:"context_model"`
		ContextWindow int    `json:"context_window"`
		LastEvent     *struct {
			Type string `json:"type"`
		} `json:"last_event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *struct {
		ID            string `json:"id"`
		ContextTokens int64  `json:"context_tokens"`
		ContextModel  string `json:"context_model"`
		ContextWindow int    `json:"context_window"`
		LastEvent     *struct {
			Type string `json:"type"`
		} `json:"last_event"`
	}
	for i := range tasks {
		if tasks[i].ID == task.TaskID {
			got = &tasks[i]
		}
	}
	if got == nil {
		t.Fatalf("task %s missing from list", task.TaskID)
	}
	if got.ContextTokens != 48_500 {
		t.Errorf("context_tokens = %d, want newest sample 48500", got.ContextTokens)
	}
	if got.ContextModel != "claude-sonnet-4-5" {
		t.Errorf("context_model = %q", got.ContextModel)
	}
	if got.ContextWindow != 200_000 {
		t.Errorf("context_window = %d, want 200000", got.ContextWindow)
	}
	if got.LastEvent == nil || got.LastEvent.Type != "progress" {
		t.Fatalf("last_event = %+v, want the progress row (a context sample must never narrate)", got.LastEvent)
	}
}

// The chat bubble's brief read carries the same numbers as the list.
func TestBriefTaskCarriesAgentContext(t *testing.T) {
	db := testdb.Open(t)
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	h := New(Deps{Store: store, DB: db, StaticDir: t.TempDir(), Token: testToken})

	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "brief task", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvAgentContext,
		map[string]any{"context_tokens": 9_000, "model": "claude-haiku-4-5"}); err != nil {
		t.Fatalf("record sample: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"?brief=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ContextTokens int64  `json:"context_tokens"`
		ContextModel  string `json:"context_model"`
		ContextWindow int    `json:"context_window"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ContextTokens != 9_000 || got.ContextModel != "claude-haiku-4-5" || got.ContextWindow != 200_000 {
		t.Fatalf("brief context = %+v, want 9000 / claude-haiku-4-5 / 200000", got)
	}
}
