// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestImmediateCoordinationRefusesNativeExecution(t *testing.T) {
	c := newCore(t, "native-coordination")
	ctx := context.Background()
	plan := commander.Plan{Kind: "native", Tier: 1, Command: os.Args[0], Args: []string{"-test.run=^$"}}
	task := Task{SpecJSON: `{"coordination":"IMMEDIATE"}`}
	runCtx, cleanup := c.decorateExecCtx(ctx, ctx, "native-coordination", task, plan, newExecEvidence(), false)
	defer cleanup()
	result := c.router.Execute(runCtx, plan, "coordinate child work", t.TempDir(), true)
	if result.OK || !strings.Contains(result.Stderr, "IMMEDIATE") {
		t.Fatalf("native execution bypassed coordination requirement: %+v", result)
	}
}

func TestChildWorkspaceUsesParentExecutionTree(t *testing.T) {
	c, ps, _ := projectCore(t)
	c.delegLimits = config.DelegationConfig{}.NormalizedDelegation()
	ctx := context.Background()
	configured, actual := t.TempDir(), t.TempDir()
	for dir, value := range map[string]string{configured: "stale configured checkout", actual: "live parent edits"} {
		if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ps.Create("demo", configured, ""); err != nil {
		t.Fatal(err)
	}
	parent, err := c.store.Create(ctx, "", "demo", "parent", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetExecWorkDir(ctx, parent.TaskID, actual); err != nil {
		t.Fatal(err)
	}
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	payload := bus.TaskDelegatePayload{TaskID: child.TaskID, Project: child.Project}
	if err := c.attachChildWorkspace(ctx, &payload, child); err != nil {
		t.Fatal(err)
	}
	if len(payload.Inputs) != 1 || payload.ProjectDir != "" {
		t.Fatalf("unsafe or missing child workspace: %+v", payload)
	}
	landed := t.TempDir()
	if _, err := c.artifacts.Extract(payload.Inputs[0].Hash, landed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(landed, "main.txt"))
	if err != nil || string(got) != "live parent edits" {
		t.Fatalf("child received stale project bytes: %q, %v", got, err)
	}
	stored, err := c.store.Get(ctx, child.TaskID)
	if err != nil || len(stored.Inputs) != 1 || stored.Inputs[0].Hash != payload.Inputs[0].Hash {
		t.Fatalf("child input not durable: %+v, %v", stored.Inputs, err)
	}
}

func runningRPCParent(t *testing.T) (*Core, Task, *childRPCServer) {
	t.Helper()
	ctx := context.Background()
	c := newCore(t, "win")
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	parent, err := c.store.Create(ctx, "", "", "parent", c.nodeID, []string{"mac", "win"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Queue(ctx, parent.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Dispatch(ctx, parent.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Accept(ctx, parent.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetDelegationBudget(ctx, parent.TaskID, 20); err != nil {
		t.Fatal(err)
	}
	parent, _ = c.store.Get(ctx, parent.TaskID)
	return c, parent, &childRPCServer{c: c, ctx: ctx, parent: parent, token: "secret"}
}

func TestChildRPCSpawnIdempotencyAndAncestry(t *testing.T) {
	c, parent, s := runningRPCParent(t)
	ctx := context.Background()
	req := ChildRPCRequest{Operation: "spawn", RequestID: "repair-1", Title: "repair", Requires: []string{"sys:info"}}
	first, err := s.spawn(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.spawn(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := first.(map[string]any)["task_id"].(string)
	if again.(map[string]any)["task_id"] != id {
		t.Fatal("retry created another child")
	}
	child, err := c.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != parent.TaskID || len(child.Chain) != 1 || child.Chain[0] != "win" || child.SpawnDepth != parent.SpawnDepth+1 {
		t.Fatalf("bad child ancestry/route: %+v", child)
	}
	req.Title = "different"
	if _, err := s.spawn(ctx, req); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	req.RequestID = "repair-2"
	req.Project = "other"
	if _, err := s.spawn(ctx, req); err == nil {
		t.Fatal("foreign project accepted")
	}
}

func TestChildRPCAuthAndAttemptFencing(t *testing.T) {
	c, parent, s := runningRPCParent(t)
	invoke := func(token, attempt string) int {
		raw, _ := json.Marshal(ChildRPCRequest{Operation: "spawn", RequestID: "one", Title: "x", AttemptID: attempt})
		req := httptest.NewRequest(http.MethodPost, "http://localhost/children", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		s.ServeHTTP(out, req)
		return out.Code
	}
	if got := invoke("wrong", parent.AttemptID); got != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", got)
	}
	if got := invoke("secret", "old"); got != http.StatusConflict {
		t.Fatalf("stale status %d", got)
	}
	if err := c.store.Cancel(context.Background(), parent.TaskID); err != nil {
		t.Fatal(err)
	}
	if got := invoke("secret", parent.AttemptID); got != http.StatusConflict {
		t.Fatalf("cancelled status %d", got)
	}
}

func TestChildRPCAwaitYieldsCapacityAndRejectsForeignChild(t *testing.T) {
	c, parent, s := runningRPCParent(t)
	ctx := context.Background()
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "child", Intent: "x"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(30 * time.Millisecond); _ = c.store.Cancel(ctx, child.TaskID) }()
	if _, err := s.await(ctx, ChildRPCRequest{TaskID: child.TaskID, TimeoutS: 1}); err != nil {
		t.Fatal(err)
	}
	var waiting string
	if err := c.store.db.QueryRow(`SELECT children_wait_attempt FROM tasks WHERE task_id=?`, parent.TaskID).Scan(&waiting); err != nil || waiting != "" {
		t.Fatalf("parent not resumed: %s %v", waiting, err)
	}
	foreign, err := c.store.Create(ctx, "someone-else", "", "foreign", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.await(ctx, ChildRPCRequest{TaskID: foreign.TaskID}); err == nil {
		t.Fatal("foreign child awaited")
	}
}

func TestChildRPCRecreatedEndpointsSerializeDecisions(t *testing.T) {
	c, parent, first := runningRPCParent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	second := &childRPCServer{c: c, ctx: ctx, parent: parent, token: "second"}
	type reply struct {
		view any
		err  error
	}
	replies := make(chan reply, 2)
	for _, endpoint := range []*childRPCServer{first, second} {
		go func(s *childRPCServer) {
			view, err := s.spawn(ctx, ChildRPCRequest{Operation: "spawn", RequestID: "same", Title: "same child", Requires: []string{"sys:info"}})
			replies <- reply{view, err}
		}(endpoint)
	}
	a, b := <-replies, <-replies
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent retry failed: %v %v", a.err, b.err)
	}
	id := a.view.(map[string]any)["task_id"].(string)
	if b.view.(map[string]any)["task_id"] != id {
		t.Fatal("recreated endpoint duplicated a child")
	}
	awaited := make(chan error, 1)
	go func() {
		_, err := first.await(ctx, ChildRPCRequest{TaskID: id, TimeoutS: 2})
		awaited <- err
	}()
	for {
		var waiting string
		if err := c.store.db.QueryRowContext(ctx, "SELECT children_wait_attempt FROM tasks WHERE task_id=?", parent.TaskID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == parent.AttemptID {
			break
		}
		select {
		case err := <-awaited:
			t.Fatalf("await ended before yielding: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := second.await(ctx, ChildRPCRequest{TaskID: id, TimeoutS: 1}); err == nil {
		t.Fatal("overlapping await acquired the parent twice")
	}
	if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "during wait", childRequestID: "during-wait", childRequestHash: "hash"}); err == nil {
		t.Fatal("child minted while parent execution was suspended")
	}
	if err := c.store.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := <-awaited; err != nil {
		t.Fatal(err)
	}
}

func TestFailResultFromRemotePreservesExecutor(t *testing.T) {
	c, task, _ := runningRPCParent(t)
	ctx := context.Background()
	result := bus.TaskResultPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, State: StateFailed, Executor: "gpu-worker", Agent: "codex", Model: "custom-model", WorkDir: "C:/work/project", Stderr: "test failed"}
	if err := c.store.FailResultFromRemote(ctx, task.TaskID, c.nodeID, result); err != nil {
		t.Fatal(err)
	}
	stored, _ := c.store.Get(ctx, task.TaskID)
	var actual bus.TaskResultPayload
	if err := json.Unmarshal([]byte(stored.ResultJSON), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Executor != result.Executor || actual.Model != result.Model || actual.WorkDir != result.WorkDir || !strings.Contains(actual.Stderr, "test failed") {
		t.Fatalf("lost failure attribution: %+v", actual)
	}
}
