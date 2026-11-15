// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/todos"
)

// TestTodosCRUD covers the to-do board's HTTP surface end to end. /start is
// exercised only for its no-engine refusal here: enqueueing a real task needs
// a configured model, and the crossing itself is covered by the queue tests
// through the shared enqueueBoardTask helper.
func TestTodosCRUD(t *testing.T) {
	h := New(Deps{
		Store:     newTestStore(t),
		Todos:     todos.NewStore(newTestDB(t)),
		StaticDir: t.TempDir(),
		Token:     testToken,
	})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"invalid json", "{not json", http.StatusBadRequest},
		{"empty title", `{"title":"   "}`, http.StatusBadRequest},
		{"bad due date", `{"title":"x","due_at":"tomorrow"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/todos", tc.body)); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/todos",
		`{"title":"写周报","description":"本周进展","priority":"high","due_at":"2026-11-02","tags":["工作"],"project":"panda"}`))
	if code != http.StatusOK {
		t.Fatalf("create status = %d, out = %v", code, out)
	}
	id, _ := out["id"].(float64)
	if id <= 0 {
		t.Fatalf("no id assigned: %v", out)
	}
	if out["status"] != todos.StatusPending || out["priority"] != "high" {
		t.Fatalf("created row = %v, want pending/high", out)
	}
	if due, _ := out["due_at"].(float64); due <= 0 {
		t.Fatalf("date-only due_at not converted to a timestamp: %v", out["due_at"])
	}

	// The board reads the whole list in one call — and an empty board must
	// serialize as [] so the client's `todos ?? []` never sees null.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/todos", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	var listed []todos.Todo
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list body = %s: %v", rr.Body.String(), err)
	}
	if len(listed) != 1 || listed[0].Title != "写周报" {
		t.Fatalf("list = %+v, want the one created todo", listed)
	}

	// Tick it off: done stamps done_at.
	code, patched := doJSON(t, h, jsonReq(http.MethodPatch, "/api/todos/1", `{"status":"done"}`))
	if code != http.StatusOK || patched["status"] != todos.StatusDone {
		t.Fatalf("patch status = %d, out = %v", code, patched)
	}
	if done, _ := patched["done_at"].(float64); done <= 0 {
		t.Fatalf("done_at not stamped: %v", patched)
	}

	// An empty title is refused, not silently applied.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPatch, "/api/todos/1", `{"title":""}`)); code != http.StatusBadRequest {
		t.Fatalf("empty title patch status = %d, want 400", code)
	}
	if code, _ := doJSON(t, h, jsonReq(http.MethodPatch, "/api/todos/999", `{"status":"done"}`)); code != http.StatusNotFound {
		t.Fatalf("missing id patch status = %d, want 404", code)
	}

	// Drag order.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/todos/reorder", `{"ids":[1]}`)); code != http.StatusOK {
		t.Fatalf("reorder status = %d", code)
	}
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/todos/reorder", `{"ids":[]}`)); code != http.StatusBadRequest {
		t.Fatalf("empty reorder status = %d, want 400", code)
	}

	// 开始 without a model is a 503 with the queue's own wording — the board
	// shows it as "configure a model first", not as a crash.
	if code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/todos/1/start", "")); code != http.StatusServiceUnavailable {
		t.Fatalf("start without engine status = %d, want 503", code)
	}

	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/todos/1", nil)); code != http.StatusOK {
		t.Fatalf("delete status = %d", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/todos/1", nil)); code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", code)
	}
}
