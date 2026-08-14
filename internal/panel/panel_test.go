package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/storage"
)

func newTestStore(t *testing.T) *core.TaskStore {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return core.NewTaskStore(db, slog.New(slog.DiscardHandler))
}

// reviewTask drives a freshly-created task into review via the normal
// queue→dispatch→accept→pause path, so approval tests start from a real review
// state rather than hand-inserted rows.
func reviewTask(t *testing.T, store *core.TaskStore) core.Task {
	t.Helper()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "reviewed", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "node", "node") },
		func() error { return store.Accept(ctx, task.TaskID, "node") },
		func() error { return store.Pause(ctx, task.TaskID, "node", "scope drift") },
	} {
		if err := step(); err != nil {
			t.Fatalf("drive to review: %v", err)
		}
	}
	return task
}

func TestListTasks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _ = store.Create(ctx, "", "proj", "task one", "node", []string{"node"})
	_, _ = store.Create(ctx, "", "other", "task two", "node", []string{"node"})

	h := New(store, t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var tasks []taskJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("len = %d, want 2", len(tasks))
	}
}

func TestListTasksFilterByProject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _ = store.Create(ctx, "", "proj", "task one", "node", []string{"node"})
	_, _ = store.Create(ctx, "", "other", "task two", "node", []string{"node"})

	h := New(store, t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks?project=proj", nil))
	var tasks []taskJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "task one" {
		t.Fatalf("filtered = %+v, want only 'task one'", tasks)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	h := New(newTestStore(t), t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestApproveReview(t *testing.T) {
	store := newTestStore(t)
	task := reviewTask(t, store)

	h := New(store, t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.TaskID+"/approve", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	got, _ := store.Get(context.Background(), task.TaskID)
	if got.State != core.StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
}

func TestRejectReview(t *testing.T) {
	store := newTestStore(t)
	task := reviewTask(t, store)

	h := New(store, t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.TaskID+"/reject?reason=nope", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	got, _ := store.Get(context.Background(), task.TaskID)
	if got.State != core.StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
}

func TestApproveNonReviewConflicts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, _ := store.Create(ctx, "", "proj", "still submitted", "node", []string{"node"})

	h := New(store, t.TempDir())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.TaskID+"/approve", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}
