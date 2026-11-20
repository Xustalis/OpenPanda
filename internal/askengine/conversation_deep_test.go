// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// A manual controller keeps sweeps deterministic while using the actual runner.
func deepManualController(t *testing.T, e *Engine, st *sessions.Store) *ConversationService {
	t.Helper()
	s := e.Conversations(st)
	s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	t.Cleanup(s.Close)
	return s
}

func TestDeepEmptyImplementationDoesNotResumeGoal(t *testing.T) {
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(anthropicText(""))
	})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("empty implementation")
	if err != nil {
		t.Fatal(err)
	}
	s := deepManualController(t, e, st)
	ctx := context.Background()
	if err = s.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, sess.ID, "implement and verify every requirement", nil); err != nil {
		t.Fatal(err)
	}
	version, err := s.store.SaveProposal(ctx, sess.ID, "implement the fixture and verify it")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	s.sweepPlans(ctx)
	s.wg.Wait()
	s.sweepPlans(ctx)
	state, err := s.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Status != "paused" {
		t.Fatalf("empty implementation resumed goal: %+v calls=%d", state.Goal, calls.Load())
	}
	persisted, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Operation == nil || persisted.Operation.Status != "error" {
		t.Fatalf("empty implementation displayed success: %+v", persisted.Operation)
	}
}

func TestDeepExplicitPauseDuringImplementationSurvivesSettlement(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(anthropicText("implementation verified"))
	})
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("pause implementation")
	if err != nil {
		t.Fatal(err)
	}
	s := deepManualController(t, e, st)
	ctx := context.Background()
	if err = s.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	g, err := s.Create(ctx, sess.ID, "verify all requirements", nil)
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.store.SaveProposal(ctx, sess.ID, "implement complete proposal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmPlan(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	s.sweepPlans(ctx)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("implementation never reached the local model")
	}
	if err = s.Control(ctx, sess.ID, g.ID, "pause", "", nil); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	s.wg.Wait()
	s.sweepPlans(ctx)
	state, err := s.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Status != "paused" || state.Goal.PausedByPlan || state.Goal.Tokens != 14 {
		t.Fatalf("pause/usage lost: %+v", state.Goal)
	}
}

func TestDeepHardProcessCrashReconcilesSurvivingWork(t *testing.T) {
	ctx := context.Background()
	if root := os.Getenv("PANDA_DEEP_CRASH_ROOT"); root != "" {
		db, err := storage.Open(filepath.Join(root, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		e, _ := newBoundaryTestEngine(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected fixture model") })
		e.db = db
		client, err := entry.NewClient(config.ModelConfig{BaseURL: os.Getenv("PANDA_DEEP_CRASH_MODEL"), Model: "local-crash-fixture", APIKey: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		e.client.Store(client)
		service := e.Conversations(sessions.NewStore(filepath.Join(root, "sessions")))
		defer service.Close()
		select {} // Parent kills this process while its real controller owns a round.
	}
	root := t.TempDir()
	db := testdb.OpenFile(t, filepath.Join(root, "state.db"))
	st := sessions.NewStore(filepath.Join(root, "sessions"))
	sess, err := st.Create("hard crash fixture")
	if err != nil {
		t.Fatal(err)
	}
	store := &conversation.Store{DB: db}
	if _, err = store.Create(ctx, sess.ID, "verify the complete objective after a hard process crash", nil); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestDeepHardProcessCrashReconcilesSurvivingWork$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PANDA_DEEP_CRASH_ROOT="+root, "PANDA_DEEP_CRASH_MODEL="+server.URL, "TMP="+root, "TEMP="+root)
	output, err := os.Create(filepath.Join(root, "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout = output
	cmd.Stderr = output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		killed = true
		b, _ := os.ReadFile(filepath.Join(root, "child.log"))
		t.Fatalf("child never called model: %s", b)
	}
	state, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldRound := state.Goal.RoundID
	if oldRound == "" {
		t.Fatal("child did not persist claimed round")
	}
	tasks := core.NewTaskStore(db, nil)
	task, err := tasks.Create(core.WithAskRound(ctx, oldRound), "", "", "surviving local task", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.Queue(ctx, task.TaskID, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = tasks.Dispatch(ctx, task.TaskID, "fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = tasks.Accept(ctx, task.TaskID, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("child was not forcibly terminated")
	}
	killed = true
	var calls atomic.Int32
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		content := []map[string]any{{"type": "text", "text": "verified every original requirement"}}
		if calls.Add(1) == 1 {
			content = []map[string]any{{"type": "tool_use", "id": "audit", "name": "update_goal", "input": map[string]any{"status": "complete", "reason": "verified the full original objective and surviving task checkpoint"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": content, "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}})
	})
	e.db = db
	s := deepManualController(t, e, st)
	s.sweep()
	if calls.Load() != 0 {
		t.Fatal("live dispatcher lease was replayed")
	}
	if _, err = db.Exec("UPDATE ask_rounds SET lease_until=0 WHERE round_id=?", oldRound); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		s.sweep()
	}
	state, err = store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || state.Goal.RoundID != oldRound || state.Goal.Rounds != 1 {
		t.Fatalf("replayed surviving work: %+v calls=%d", state.Goal, calls.Load())
	}
	if err = tasks.Complete(ctx, task.TaskID, "fixture", map[string]any{"ok": true, "checkpoint": "done once"}); err != nil {
		t.Fatal(err)
	}
	s.sweep()
	s.sweep()
	s.wg.Wait()
	s.sweep()
	state, err = store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Status != "complete" || state.Goal.Rounds != 2 || state.Goal.Tokens != 20 || calls.Load() != 2 {
		t.Fatalf("recovery/audit/usage: %+v calls=%d", state.Goal, calls.Load())
	}
	for i := 0; i < 4; i++ {
		s.sweep()
	}
	state, err = store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Tokens != 20 || calls.Load() != 2 {
		t.Fatal("repeated notification replayed execution or billing")
	}
}

func TestDeepTaskUsageArrivingAfterSettlementRemainsAccounted(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(http.ResponseWriter, *http.Request) { t.Error("no model expected") })
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("late task metrics")
	if err != nil {
		t.Fatal(err)
	}
	s := deepManualController(t, e, st)
	ctx := context.Background()
	budget := int64(20)
	g, err := s.Create(ctx, sess.ID, "verify all original work", &budget)
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID, err = s.store.Claim(ctx, g, "fixture-owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	task, err := e.TaskStore().Create(core.WithAskRound(ctx, g.RoundID), "", "", "task metrics race", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().Cancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
		t.Fatal(err)
	}
	s.reconcile(ctx, g)
	// The wire path publishes terminal task state before inserting its metric.
	if err = e.TaskStore().RecordDelegationMetric(ctx, task.TaskID, "fixture", "executor", nil, true, 1, 13); err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().RecordDelegationMetric(ctx, task.TaskID, "fixture", "entry:supervisor", nil, true, 1, 7); err != nil {
		t.Fatal(err)
	}
	if err = e.TaskStore().RecordAskMetric(ctx, task.TaskID, "fixture", "entry:ask", true, 1, 99, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s.reconcile(ctx, g)
	}
	state, err := s.State(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Tokens != 20 || state.Goal.Status != "budget_limited" {
		t.Fatalf("late task/supervisor usage lost or ask double charged: %+v", state.Goal)
	}
	var ledger int64
	if err = e.db.QueryRow("SELECT entry_tokens+task_tokens FROM conversation_goal_rounds WHERE round_id=?", g.RoundID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 20 {
		t.Fatalf("ledger=%d", ledger)
	}
}

func TestDeepConcurrentMetricWritersAndSettlement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db := testdb.OpenFile(t, path)
	s := &conversation.Store{DB: db}
	budget := int64(60)
	g, err := s.Create(ctx, "session", "all required work", &budget)
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID, err = s.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	tasks := core.NewTaskStore(db, nil)
	root, err := tasks.Create(core.WithAskRound(ctx, g.RoundID), "", "", "root", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := tasks.Create(ctx, root.TaskID, "", "child", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.Cancel(ctx, root.TaskID); err != nil {
		t.Fatal(err)
	}
	if err = tasks.CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		handle, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { handle.Close() })
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var err error
			switch i {
			case 8:
				err = (&conversation.Store{DB: handle}).Finish(ctx, g.RoundID, 11)
			case 9:
				err = (&conversation.Store{DB: handle}).Settle(ctx, g, 0)
			default:
				err = core.NewTaskStore(handle, nil).RecordDelegationMetric(ctx, root.TaskID, "fixture", "entry:supervisor", nil, true, 1, i+1)
			}
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if err = tasks.RecordDelegationMetric(ctx, child.TaskID, "fixture", "executor", nil, true, 1, 13); err != nil {
		t.Fatal(err)
	}
	if err = tasks.RecordAskMetric(ctx, root.TaskID, "fixture", "entry:ask", true, 1, 99, 0); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Tokens != 60 || state.Goal.Status != "budget_limited" {
		t.Fatalf("concurrent usage: %+v", state.Goal)
	}
	var ledger int64
	if err = db.QueryRow("SELECT entry_tokens+task_tokens FROM conversation_goal_rounds WHERE round_id=?", g.RoundID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 60 {
		t.Fatalf("ledger=%d", ledger)
	}
}
