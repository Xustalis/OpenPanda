package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/storage"
	"github.com/xenith/openpanda/webui/push"
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

// testToken is the Bearer credential the auth tests use. Production must set
// network.panel_token; here a fixed value keeps assertions simple.
const testToken = "test-token"

// authedReq builds a request carrying the test token, so authenticated endpoint
// tests exercise the real auth path rather than bypassing it.
func authedReq(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
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

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks", nil))
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

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks?project=proj", nil))
	var tasks []taskJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "task one" {
		t.Fatalf("filtered = %+v, want only 'task one'", tasks)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestApproveReview(t *testing.T) {
	store := newTestStore(t)
	task := reviewTask(t, store)

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/approve", nil))
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

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/reject?reason=nope", nil))
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

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/approve", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

func newTestPushService(t *testing.T) *push.Service {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	keys, err := push.GenerateVAPIDKeys("mailto:test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return push.NewService(keys, push.NewStore(db), slog.New(slog.DiscardHandler))
}

func TestPushKey(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), newTestPushService(t), testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/push/key", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var m map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["key"] == "" {
		t.Fatal("empty applicationServerKey")
	}
}

// validSubJSON builds a well-formed subscription body with 65-byte p256dh and
// 16-byte auth keys.
func validSubJSON() string {
	p256dh := make([]byte, 65)
	p256dh[0] = 0x04
	_, _ = rand.Read(p256dh[1:])
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return fmt.Sprintf(`{"endpoint":"https://example.com/push/1","keys":{"p256dh":%q,"auth":%q}}`,
		base64.RawURLEncoding.EncodeToString(p256dh),
		base64.RawURLEncoding.EncodeToString(auth))
}

func TestPushSubscribeRejectsInvalid(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), newTestPushService(t), testToken)
	rr := httptest.NewRecorder()
	req := authedReq(http.MethodPost, "/api/push/subscribe", nil)
	req.Body = io.NopCloser(strings.NewReader(`{"endpoint":"https://example.com/push/1","keys":{"p256dh":"short","auth":"short"}}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestPushSubscribeUnsubscribeRoundTrip(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), newTestPushService(t), testToken)
	// subscribe
	rr := httptest.NewRecorder()
	req := authedReq(http.MethodPost, "/api/push/subscribe", nil)
	req.Body = io.NopCloser(strings.NewReader(validSubJSON()))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d, body %s", rr.Code, rr.Body.String())
	}
	// unsubscribe
	rr = httptest.NewRecorder()
	req = authedReq(http.MethodPost, "/api/push/unsubscribe", nil)
	req.Body = io.NopCloser(strings.NewReader(`{"endpoint":"https://example.com/push/1"}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d, body %s", rr.Code, rr.Body.String())
	}
}

func TestAPIAuthRequired(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil)) // no header
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAPIWrongToken(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), nil, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestEmptyTokenFailsClosed(t *testing.T) {
	// A panel configured without a token must never serve /api/* — fail closed
	// even if a client supplies a Bearer header.
	h := New(newTestStore(t), t.TempDir(), nil, "")
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (fail closed)", rr.Code)
	}
}

func TestStaticServedWithoutAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(newTestStore(t), dir, nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil)) // no header
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (static assets are not gated)", rr.Code)
	}
}

// TestApproveErrorDoesNotLeakInternals verifies the panel maps internal state
// errors to a generic message (P2-15): the task id, internal state name, or
// underlying store error text must never reach the HTTP client.
func TestApproveErrorDoesNotLeakInternals(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, _ := store.Create(ctx, "", "proj", "internal-secret-detail", "node", []string{"node"})

	h := New(store, t.TempDir(), nil, testToken)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/approve", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	for _, leak := range []string{task.TaskID, "state=", "want review", "sqlite", "ErrConflict"} {
		if strings.Contains(body, leak) {
			t.Fatalf("error body leaked internal detail %q: %q", leak, body)
		}
	}
}

// TestAuthRateLimited verifies the per-IP failure budget (L1): after
// maxAuthFailures wrong tokens the IP is locked out with 429, while other IPs
// are unaffected.
func TestAuthRateLimited(t *testing.T) {
	h := New(newTestStore(t), t.TempDir(), nil, testToken)

	badAttempt := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	for i := 0; i < maxAuthFailures; i++ {
		if rr := badAttempt(); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rr.Code)
		}
	}
	if rr := badAttempt(); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after %d failures", rr.Code, maxAuthFailures)
	}

	// A different client IP still has its own budget and can authenticate.
	req := authedReq(http.MethodGet, "/api/tasks", nil)
	req.RemoteAddr = "198.51.100.7:9999"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("other ip: status = %d, want 200", rr.Code)
	}
}

// TestAuthRateLimitWindowReset verifies the lockout expires with its window.
func TestAuthRateLimitWindowReset(t *testing.T) {
	old := authFailWindow
	authFailWindow = 50 * time.Millisecond
	t.Cleanup(func() { authFailWindow = old })

	h := New(newTestStore(t), t.TempDir(), nil, testToken)
	for i := 0; i < maxAuthFailures; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	time.Sleep(2 * authFailWindow)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d after window reset, want 200", rr.Code)
	}
}
