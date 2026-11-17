// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func failedReviewTask(t *testing.T, store *core.TaskStore, owner, requires, reason string) core.Task {
	t.Helper()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "retry task", owner, []string{owner})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error {
			return store.SetDetail(ctx, task.TaskID, core.TaskDetail{Intent: "retry task", Requires: []string{requires}})
		},
		func() error { return store.SetAuthorized(ctx, task.TaskID, true) },
		func() error { return store.Queue(ctx, task.TaskID, owner) },
		func() error { return store.Dispatch(ctx, task.TaskID, owner, owner) },
		func() error { return store.Accept(ctx, task.TaskID, owner) },
		func() error { return store.Fail(ctx, task.TaskID, owner, reason) },
		func() error { return store.Review(ctx, task.TaskID, owner, reason) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	task, err = store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestReviewFailureVisibleOnEveryReadPath(t *testing.T) {
	db := newTestDB(t)
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	task := failedReviewTask(t, store, "node", "code:modify", "api_retry error 429: rate_limited; api_key=hidden-fixture-secret")
	h := New(Deps{Store: store, DB: db, Token: testToken, StaticDir: t.TempDir()})
	for _, path := range []string{"/api/tasks", "/api/tasks?brief=1", "/api/tasks/" + task.TaskID, "/api/tasks/" + task.TaskID + "?brief=1"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, authedReq(http.MethodGet, path, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("read: %d %s", rr.Code, rr.Body.String())
			}
			var row taskJSON
			if strings.HasPrefix(path, "/api/tasks?") || path == "/api/tasks" {
				var rows []taskJSON
				if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				row = rows[0]
			} else {
				if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
					t.Fatal(err)
				}
			}
			if row.ReviewCause != "rate_limited" || !row.RetryAvailable || !strings.Contains(row.ReviewReason, "429") || strings.Contains(row.ReviewReason, "hidden-fixture-secret") {
				t.Fatalf("missing or unsafe error metadata: cause=%s retry=%v reason=%q", row.ReviewCause, row.RetryAvailable, row.ReviewReason)
			}
		})
	}
}

func TestRetryTaskEndpointExecutesFailedTask(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	seed, _, err := engine.EnqueueTask(context.Background(), core.TaskInput{Title: "identity fixture", Intent: "identity fixture", Requires: []string{"test:once"}}, core.DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	task := failedReviewTask(t, store, seed.OwnerNode, "test:once", "api_retry error 429: rate_limited")
	h := New(Deps{Store: store, Engine: engine, Token: testToken, StaticDir: t.TempDir()})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/retry", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", rr.Code, rr.Body.String())
	}
	var op approvalOperation
	if err := json.Unmarshal(rr.Body.Bytes(), &op); err != nil || op.ID == "" {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		current, err := store.Get(context.Background(), task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == core.StateDone {
			if current.AttemptID == task.AttemptID || !current.Authorized {
				t.Fatalf("retry did not execute with original consent: %+v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			operation := httptest.NewRecorder()
			h.ServeHTTP(operation, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"/operations/"+op.ID, nil))
			t.Fatalf("retry did not finish: %+v operation=%s", current, operation.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "x\n" {
		t.Fatalf("native execution count=%q err=%v", data, err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/retry", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("completed retry: %d %s", rr.Code, rr.Body.String())
	}
}

func TestRetryTaskRefusesPolicyParkAndMissingEngine(t *testing.T) {
	db := newTestDB(t)
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	task := failedReviewTask(t, store, "node", "code:modify", "api_retry error 429: rate_limited")
	policy := reviewTask(t, store)
	h := New(Deps{Store: store, DB: db, Token: testToken, StaticDir: t.TempDir()})
	for _, tc := range []struct {
		id     string
		status int
	}{{task.TaskID, http.StatusServiceUnavailable}, {policy.TaskID, http.StatusConflict}} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+tc.id+"/retry", nil))
		if rr.Code != tc.status {
			t.Fatalf("retry %s: %d %s", tc.id, rr.Code, rr.Body.String())
		}
	}
}
