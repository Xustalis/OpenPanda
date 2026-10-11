// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// withTestCache swaps the package-wide SSE fingerprint cache for one driven by
// a fake clock, so TTL behaviour is deterministic. The real cache is restored
// when the test ends — every handler-level test in this package gets a fresh,
// isolated cache, so no test inherits another's stale fingerprints.
func withTestCache(t *testing.T) *time.Time {
	t.Helper()
	prev := sharedFingerprints
	clock := time.Unix(1_000_000, 0)
	c := newFingerprintCache(eventsPollInterval)
	c.now = func() time.Time { return clock }
	sharedFingerprints = c
	t.Cleanup(func() { sharedFingerprints = prev })
	return &clock
}

func TestFingerprintCacheServesWithinTTL(t *testing.T) {
	c := newFingerprintCache(time.Second)
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }

	calls := 0
	load := func() (string, error) { calls++; return "fp", nil }

	if got, err := c.get("k", load); err != nil || got != "fp" || calls != 1 {
		t.Fatalf("first get = %q, %v, calls = %d; want fp, nil, 1", got, err, calls)
	}
	clock = clock.Add(900 * time.Millisecond) // still inside the TTL window
	if got, err := c.get("k", load); err != nil || got != "fp" || calls != 1 {
		t.Fatalf("cached get = %q, %v, calls = %d; want fp, nil, 1", got, err, calls)
	}
}

func TestFingerprintCacheExpiresAfterTTL(t *testing.T) {
	c := newFingerprintCache(time.Second)
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }

	calls := 0
	load := func() (string, error) { calls++; return "fp", nil }

	if _, err := c.get("k", load); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clock = clock.Add(2 * time.Second) // past the TTL: must rescan
	if _, err := c.get("k", load); err != nil || calls != 2 {
		t.Fatalf("expired get err = %v, calls = %d; want nil, 2", err, calls)
	}
}

func TestFingerprintCacheConcurrentSingleFlight(t *testing.T) {
	c := newFingerprintCache(time.Second)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	calls := 0
	var countMu sync.Mutex
	load := func() (string, error) {
		countMu.Lock()
		calls++
		countMu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "fp", nil
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	vals := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vals[i], errs[i] = c.get("k", load)
		}(i)
	}
	<-started                         // the first worker is inside load()
	time.Sleep(20 * time.Millisecond) // let the rest pile up behind it
	close(release)
	wg.Wait()

	countMu.Lock()
	defer countMu.Unlock()
	if calls != 1 {
		t.Fatalf("underlying load ran %d times; want 1 (single-flight)", calls)
	}
	for i := range workers {
		if errs[i] != nil || vals[i] != "fp" {
			t.Fatalf("worker %d = %q, %v; want fp, nil", i, vals[i], errs[i])
		}
	}
}

func TestFingerprintCacheErrorNotCached(t *testing.T) {
	c := newFingerprintCache(time.Second)
	calls := 0
	load := func() (string, error) {
		calls++
		if calls == 1 {
			return "", context.DeadlineExceeded
		}
		return "fp", nil
	}
	if _, err := c.get("k", load); err == nil {
		t.Fatal("first get: want error, got nil")
	}
	// A failed scan must not pin an error for the whole window: the very next
	// caller retries immediately.
	if got, err := c.get("k", load); err != nil || got != "fp" || calls != 2 {
		t.Fatalf("retry = %q, %v, calls = %d; want fp, nil, 2", got, err, calls)
	}
}

// A load that fails while other callers are piled up behind it must hand every
// waiter the same error, not an empty value with a nil error: the events loop
// treats a nil-error empty fingerprint as a change signal, so the broken window
// would fan a false change event out to every connected stream while only the
// loader's own stream dropped.
func TestFingerprintCacheWaitersShareLoadError(t *testing.T) {
	c := newFingerprintCache(time.Second)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	wantErr := context.DeadlineExceeded
	load := func() (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "", wantErr
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	vals := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vals[i], errs[i] = c.get("k", load)
		}(i)
	}
	<-started                         // the first worker is inside load()
	time.Sleep(20 * time.Millisecond) // let the rest pile up behind it
	close(release)
	wg.Wait()

	for i := range workers {
		if !errors.Is(errs[i], wantErr) || vals[i] != "" {
			t.Fatalf("worker %d = %q, %v; want \"\", the loader's error", i, vals[i], errs[i])
		}
	}
}

// The integration view of the requirement: within one poll window the
// underlying store scan happens once, no matter how many handlers (i.e. SSE
// connections) ask for the fingerprint.
func TestTaskFingerprintCachedWithinPollWindow(t *testing.T) {
	clock := withTestCache(t)
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, "", "proj", "task one", "node", []string{"node"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h := &handler{store: store}
	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)

	first, err := h.taskFingerprint(r)
	if err != nil || first == "" {
		t.Fatalf("first fingerprint = %q, %v", first, err)
	}

	// Mutate the task set; the fingerprint must stay frozen for the TTL,
	// proving the second read came from the shared cache, not the store.
	if _, err := store.Create(ctx, "", "proj", "task two", "node", []string{"node"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	second, err := h.taskFingerprint(r)
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if second != first {
		t.Fatalf("fingerprint changed within TTL: %q -> %q", first, second)
	}

	// Past the TTL the next poll rescans and sees the new task.
	*clock = clock.Add(2 * eventsPollInterval)
	third, err := h.taskFingerprint(r)
	if err != nil {
		t.Fatalf("third fingerprint: %v", err)
	}
	if third == first {
		t.Fatal("fingerprint unchanged after TTL expiry despite a new task")
	}
}

// Two handlers standing in for two live SSE connections must observe the same
// shared cache: one store scan serves both, and they agree on the digest.
func TestTaskFingerprintSharedAcrossHandlers(t *testing.T) {
	withTestCache(t)
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, "", "proj", "task one", "node", []string{"node"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h1 := &handler{store: store}
	h2 := &handler{store: store}
	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)

	fp1, err := h1.taskFingerprint(r)
	if err != nil {
		t.Fatalf("h1 fingerprint: %v", err)
	}

	// A second connection arriving inside the same window reads the cache —
	// even a store mutation that landed meanwhile cannot change its answer.
	if _, err := store.Create(ctx, "", "proj", "task two", "node", []string{"node"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	fp2, err := h2.taskFingerprint(r)
	if err != nil {
		t.Fatalf("h2 fingerprint: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("connections disagree within one window: %q vs %q", fp1, fp2)
	}
}

// A dead store must surface the error to the caller (which drops the stream)
// instead of being swallowed by the cache; the next poll retries.
func TestCachedTaskFingerprintPropagatesStoreError(t *testing.T) {
	clock := withTestCache(t)
	// Build the store by hand so the test owns the DB handle and can kill it.
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	h := &handler{store: store}
	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)

	if _, err := h.taskFingerprint(r); err != nil {
		t.Fatalf("warm-up fingerprint: %v", err)
	}
	// Expire the warm entry, then kill the store: the rescan must fail loudly.
	*clock = clock.Add(2 * eventsPollInterval)
	db.Close()
	if _, err := h.taskFingerprint(r); err == nil {
		t.Fatal("want error after store closed, got nil")
	}
}

// With no DB handle the node fingerprint short-circuits before the cache and
// stays empty — same contract as before the cache was introduced.
func TestCachedNodeFingerprintWithoutDB(t *testing.T) {
	withTestCache(t)
	h := &handler{store: newTestStore(t)}
	if got := h.nodeFingerprint(); got != "" {
		t.Fatalf("nodeFingerprint without db = %q, want empty", got)
	}
}

// With no reminder store the reminder fingerprint short-circuits to empty.
func TestCachedReminderFingerprintWithoutStore(t *testing.T) {
	withTestCache(t)
	h := &handler{store: newTestStore(t)}
	if got := h.cachedReminderFingerprint(); got != "" {
		t.Fatalf("reminderFingerprint without store = %q, want empty", got)
	}
}

// clipTraceData bounds pushed trace payloads: over-long strings truncate in
// place while the shape — names, ids, flags, short args — survives intact so
// the client can still switch on ev/name.
func TestClipTraceData(t *testing.T) {
	long := strings.Repeat("x", traceDataClip*2)
	got := clipTraceData(map[string]any{
		"ev":      "tool_result",
		"name":    "bash",
		"content": long,
		"input":   map[string]any{"command": long, "argc": 3},
		"list":    []any{long, "short"},
	})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("map not preserved: %T", got)
	}
	if m["ev"] != "tool_result" || m["name"] != "bash" {
		t.Fatalf("small fields altered: %v", m)
	}
	if s := m["content"].(string); len(s) > traceDataClip+3 {
		t.Fatalf("content not clipped: %d", len(s))
	}
	inner := m["input"].(map[string]any)
	if s := inner["command"].(string); len(s) > traceDataClip+3 {
		t.Fatalf("nested command not clipped: %d", len(s))
	}
	if inner["argc"] != 3 {
		t.Fatalf("numeric field altered: %v", inner)
	}
	list := m["list"].([]any)
	if s := list[0].(string); len(s) > traceDataClip+3 {
		t.Fatalf("array element not clipped: %d", len(s))
	}
	if list[1] != "short" {
		t.Fatalf("short element altered: %v", list)
	}
}

// The live-narrative set must include the agent-activity stream — without it
// the queue's live line falls back to a bare "executing" for minutes at a
// time even though task_events is full of real work.
func TestTraceEventTypesCoverAgentActivity(t *testing.T) {
	for _, typ := range []string{
		"classify_result", "route_decision", "delegation_hop",
		"exec_agent_start", "supervision_round", "tier2_triggered",
		"plan_stage_changed", "artifact_transfer",
		"agent_event", "progress", "agent_usage", "agent_fallback",
		"judge_start", "supervise", "result",
	} {
		if !traceEventTypes[typ] {
			t.Fatalf("traceEventTypes missing %q", typ)
		}
	}
}

// listTasks stamps each task's newest event so the board can narrate a
// mid-flight task on first paint — before the SSE trace stream's first frame.
func TestListTasksStampsLastEvent(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	h := New(Deps{Store: store, DB: db, StaticDir: t.TempDir(), Token: testToken})

	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "live task", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// An older orbit event followed by a fresh agent-activity row — the stamp
	// must be the newest row, whatever its type.
	if err := store.RecordEvent(ctx, task.TaskID, "exec_agent_start",
		map[string]any{"agent": "opencode", "adapter": "opencode.py"}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	big := strings.Repeat("y", traceDataClip*3)
	if err := store.RecordEvent(ctx, task.TaskID, "agent_event",
		map[string]any{"ev": "tool_use", "name": "bash", "input": map[string]any{"command": big}}); err != nil {
		t.Fatalf("record tool_use: %v", err)
	}

	req := authedReq(http.MethodGet, "/api/tasks", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var tasks []struct {
		ID        string `json:"id"`
		LastEvent *struct {
			ID   int64          `json:"id"`
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		} `json:"last_event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *struct {
		ID        string `json:"id"`
		LastEvent *struct {
			ID   int64          `json:"id"`
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		} `json:"last_event"`
	}
	for i := range tasks {
		if tasks[i].ID == task.TaskID {
			got = &tasks[i]
		}
	}
	if got == nil || got.LastEvent == nil {
		t.Fatalf("task %s missing last_event stamp", task.TaskID)
	}
	if got.LastEvent.Type != "agent_event" {
		t.Fatalf("last_event type = %q, want agent_event", got.LastEvent.Type)
	}
	input, _ := got.LastEvent.Data["input"].(map[string]any)
	if s, _ := input["command"].(string); len(s) > traceDataClip+3 {
		t.Fatalf("last_event payload not clipped: %d", len(s))
	}
}
