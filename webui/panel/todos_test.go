// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/todos"
)

// A date-only deadline is the last second of that local calendar day, which
// is not midnight+24h on a DST switch: those days are 23 or 25 hours long.
func TestEndOfLocalDay(t *testing.T) {
	for _, tc := range []struct {
		zone, date string
		span       time.Duration // length of the local calendar day
	}{
		{"Asia/Shanghai", "2026-03-08", 24 * time.Hour},
		{"America/New_York", "2026-03-08", 23 * time.Hour},                   // spring forward
		{"America/New_York", "2026-11-01", 25 * time.Hour},                   // fall back
		{"Australia/Lord_Howe", "2026-10-04", 23*time.Hour + 30*time.Minute}, // half-hour shift
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatalf("load %s: %v", tc.zone, err)
			}
			// Parsed without a location, exactly as parseDueAt does it.
			day, err := time.Parse(dateOnlyLayout, tc.date)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.date, err)
			}
			got := endOfLocalDay(day, loc)
			if y, m, d := got.In(loc).Date(); y != day.Year() || m != day.Month() || d != day.Day() {
				t.Fatalf("end of day slid off %s: %s", tc.date, got.In(loc).Format(time.RFC3339))
			}
			if h, min, s := got.Clock(); h != 23 || min != 59 || s != 59 {
				t.Fatalf("end of day = %s, want 23:59:59 local", got.Format(time.RFC3339))
			}
			// One second before the next local midnight, and the day spans
			// what the zone's offset change says it should.
			next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
			if !got.Equal(next.Add(-time.Second)) {
				t.Fatalf("end of day = %s, want %s", got, next.Add(-time.Second))
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
			if span := next.Sub(start); span != tc.span {
				t.Fatalf("calendar day spans %v, want %v", span, tc.span)
			}
		})
	}
}

// Zones that start DST at midnight have no 00:00 on the switch day. The
// deadline must still land on the date the user typed, not the evening before
// — which is what parsing the date in the local zone produces.
func TestParseDueAtMidnightDST(t *testing.T) {
	for _, tc := range []struct{ zone, date string }{
		{"America/New_York", "2026-03-08"}, // control: midnight exists
		{"America/Havana", "2026-03-08"},   // DST starts at 00:00
		{"America/Santiago", "2026-09-06"}, // DST starts at 00:00
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatalf("load %s: %v", tc.zone, err)
			}
			value, err := parseDueAtInLocation(tc.date, loc)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.date, err)
			}
			got := time.Unix(value, 0).In(loc)
			if got.Format(dateOnlyLayout) != tc.date || got.Format("15:04:05") != "23:59:59" {
				t.Fatalf("deadline for %s landed on %s, want %sT23:59:59",
					tc.date, got.Format(time.RFC3339), tc.date)
			}
		})
	}
}

func TestParseDueAt(t *testing.T) {
	if got, err := parseDueAt("  "); err != nil || got != 0 {
		t.Fatalf("empty = %d, %v; want 0, nil", got, err)
	}
	if got, err := parseDueAt("2026-03-08T12:30:00Z"); err != nil || got != time.Date(2026, 3, 8, 12, 30, 0, 0, time.UTC).Unix() {
		t.Fatalf("RFC3339 = %d, %v", got, err)
	}
	if _, err := parseDueAt("tomorrow"); err == nil {
		t.Fatal("a bare word must not parse as a due date")
	}
	day, _ := time.Parse(dateOnlyLayout, "2026-11-02")
	got, err := parseDueAt("2026-11-02")
	if err != nil || got != endOfLocalDay(day, time.Local).Unix() {
		t.Fatalf("date-only = %d, %v; want end of %s", got, err, day.Format(dateOnlyLayout))
	}
}

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
